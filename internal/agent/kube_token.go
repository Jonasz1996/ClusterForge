package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ownerAnnotation zegt op een ServiceAccount van ClusterForge wie de
// kubeconfig kreeg.
const ownerAnnotation = "clusterforge.io/gebruiker"

// token geeft een token met een vervaldatum voor een ServiceAccount van
// ClusterForge, of haalt dat ServiceAccount weg. De agent maakt de
// objecten zelf, met een vaste namespace, een naam volgens
// KubeAccountPattern en een van de twee rollen: een fout in de server kan
// zo nooit een token vragen voor een ServiceAccount van Kubernetes zelf, of
// een andere rol geven.
func (b *kubeBridge) token(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t := req.Token
	if t == nil || !protocol.KubeAccountPattern.MatchString(t.Account) {
		return protocol.KubeResponse{Error: "ongeldige naam van een ServiceAccount"}
	}
	ns := protocol.KubeAccessNamespace
	accountPath := "/api/v1/namespaces/" + ns + "/serviceaccounts/" + t.Account
	bindingPath := "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/" + protocol.KubeBindingName(t.Account)
	if t.Revoke {
		// Zonder ServiceAccount weigert de API elk token ervan meteen.
		for _, p := range []string{bindingPath, accountPath} {
			status, body, res := b.send(ctx, http.MethodDelete, p, nil, nil, "")
			if res != nil {
				return *res
			}
			if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNotFound {
				return kubeAnswer(kubeTarget{}, status, body)
			}
		}
		return protocol.KubeResponse{Status: http.StatusOK, Body: json.RawMessage(`{}`)}
	}
	clusterRole, ok := protocol.KubeAccessRoles[t.Role]
	if !ok {
		return protocol.KubeResponse{Error: fmt.Sprintf("onbekende rol %q", t.Role)}
	}
	if d := time.Duration(t.Seconds) * time.Second; d < protocol.KubeTokenMin || d > protocol.KubeTokenMax {
		return protocol.KubeResponse{Error: fmt.Sprintf("een token geldt minstens %s en hoogstens %s", protocol.KubeTokenMin, protocol.KubeTokenMax)}
	}
	if len(t.Owner) > 256 {
		return protocol.KubeResponse{Error: "de naam van de gebruiker is te lang"}
	}
	labels := map[string]any{"app.kubernetes.io/managed-by": protocol.KubeFieldManager}
	objects := []struct {
		path string
		obj  map[string]any
	}{
		{"/api/v1/namespaces/" + ns, map[string]any{
			"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "labels": labels},
		}},
		{accountPath, map[string]any{
			"apiVersion": "v1", "kind": "ServiceAccount",
			"metadata": map[string]any{"name": t.Account, "namespace": ns, "labels": labels, "annotations": map[string]any{ownerAnnotation: t.Owner}},
		}},
		{bindingPath, map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
			"metadata": map[string]any{"name": protocol.KubeBindingName(t.Account), "labels": labels},
			"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": clusterRole},
			"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": t.Account, "namespace": ns}},
		}},
	}
	q := url.Values{"fieldManager": {protocol.KubeFieldManager}}
	for _, o := range objects {
		body, err := json.Marshal(o.obj)
		if err != nil {
			return protocol.KubeResponse{Error: err.Error()}
		}
		status, out, res := b.send(ctx, http.MethodPatch, o.path, q, body, "application/apply-patch+yaml")
		if res != nil {
			return *res
		}
		if status != http.StatusOK && status != http.StatusCreated {
			return kubeAnswer(kubeTarget{}, status, out)
		}
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest", "spec": map[string]any{"expirationSeconds": t.Seconds},
	})
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	status, out, res := b.send(ctx, http.MethodPost, accountPath+"/token", nil, body, "application/json")
	if res != nil {
		return *res
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return kubeAnswer(kubeTarget{}, status, out)
	}
	var tr struct {
		Status struct {
			Token               string    `json:"token"`
			ExpirationTimestamp time.Time `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &tr); err != nil || tr.Status.Token == "" {
		return protocol.KubeResponse{Status: status, Error: "de API gaf geen token terug"}
	}
	b.mu.Lock()
	ca := b.ca
	b.mu.Unlock()
	result, err := json.Marshal(protocol.KubeTokenResult{Token: tr.Status.Token, Expires: tr.Status.ExpirationTimestamp, CA: ca})
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	return protocol.KubeResponse{Status: status, Body: result}
}
