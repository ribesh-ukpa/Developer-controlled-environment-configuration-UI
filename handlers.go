package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const reqTimeout = 15 * time.Second

// handleNamespaces returns the configured allow-list. The UI never discovers
// namespaces on its own — it only ever offers what the operator permitted.
func (a *App) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"namespaces":   a.cfg.AllowedNamespaces,
		"allowSecrets": a.cfg.AllowSecrets,
	})
}

// handleList returns the names of configmaps or secrets in a namespace.
func (a *App) handleList(w http.ResponseWriter, r *http.Request) {
	ns, kind, ok := a.parseNsKind(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reqTimeout)
	defer cancel()

	var names []string
	switch kind {
	case "configmaps":
		list, err := a.k8s.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		for _, cm := range list.Items {
			names = append(names, cm.Name)
		}
	case "secrets":
		list, err := a.k8s.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		// Skip service-account tokens and other system secrets — devs only
		// care about Opaque config secrets.
		for _, s := range list.Items {
			if s.Type == corev1.SecretTypeOpaque {
				names = append(names, s.Name)
			}
		}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
}

// handleGet returns the key/value data of a single resource. Secret values are
// base64-decoded to plain text for editing (these are dev-only Opaque secrets).
func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	ns, kind, ok := a.parseNsKind(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), reqTimeout)
	defer cancel()

	data := map[string]string{}
	switch kind {
	case "configmaps":
		cm, err := a.k8s.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			a.k8sErr(w, err)
			return
		}
		data = cm.Data
	case "secrets":
		s, err := a.k8s.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			a.k8sErr(w, err)
			return
		}
		for k, v := range s.Data {
			data[k] = string(v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace": ns, "kind": kind, "name": name, "data": data,
	})
}

type updateBody struct {
	Data    map[string]string `json:"data"`
	DevName string            `json:"devName"`
}

// handleUpdate replaces the data of a resource with the submitted key/value set
// and records an audit entry. It fetches the current object first so it can log
// exactly which keys changed.
func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ns, kind, ok := a.parseNsKind(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	var body updateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Data == nil {
		writeErr(w, http.StatusBadRequest, "missing data")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), reqTimeout)
	defer cancel()

	var changed []string
	switch kind {
	case "configmaps":
		cm, err := a.k8s.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			a.k8sErr(w, err)
			return
		}
		changed = diffKeys(cm.Data, body.Data)
		cm.Data = body.Data
		if _, err := a.k8s.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			a.k8sErr(w, err)
			return
		}
	case "secrets":
		s, err := a.k8s.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			a.k8sErr(w, err)
			return
		}
		old := map[string]string{}
		for k, v := range s.Data {
			old[k] = string(v)
		}
		changed = diffKeys(old, body.Data)
		// StringData is encoded to Data by the API server on write.
		s.Data = nil
		s.StringData = body.Data
		if _, err := a.k8s.CoreV1().Secrets(ns).Update(ctx, s, metav1.UpdateOptions{}); err != nil {
			a.k8sErr(w, err)
			return
		}
	}

	a.logAudit(auditEntry{
		DevName:     firstNonEmpty(body.DevName, "unknown"),
		RemoteAddr:  r.RemoteAddr,
		Action:      "update",
		Namespace:   ns,
		Kind:        kind,
		Name:        name,
		ChangedKeys: changed,
	})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changedKeys": changed})
}

// parseNsKind extracts and validates the {ns} and {kind} path values.
func (a *App) parseNsKind(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	ns := r.PathValue("ns")
	kind := r.PathValue("kind")
	if !a.nsAllowed(ns) {
		writeErr(w, http.StatusForbidden, "namespace not allowed")
		return "", "", false
	}
	if !a.kindAllowed(kind) {
		writeErr(w, http.StatusForbidden, "resource kind not allowed")
		return "", "", false
	}
	return ns, kind, true
}

// k8sErr maps Kubernetes API errors to sensible HTTP statuses.
func (a *App) k8sErr(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeErr(w, http.StatusNotFound, "resource not found")
	case apierrors.IsForbidden(err):
		writeErr(w, http.StatusForbidden, "kubernetes RBAC denied this action")
	case apierrors.IsConflict(err):
		writeErr(w, http.StatusConflict, "resource changed concurrently — reload and retry")
	default:
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

// diffKeys returns the set of keys that were added, removed or changed.
func diffKeys(old, new map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for k, v := range new {
		if ov, ok := old[k]; !ok || ov != v {
			if !seen[k] {
				out = append(out, k)
				seen[k] = true
			}
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
