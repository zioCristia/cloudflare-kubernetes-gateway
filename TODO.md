Read [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/README.md#1-1), lines 1 to 100

Read [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/README.md#100-100), lines 100 to 112

Now let me check the Makefile for local testing options: 

Read [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/Makefile#1-1), lines 1 to 80

Read [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/Makefile#80-80), lines 80 to 160

Read [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/Makefile#160-160), lines 160 to 200

Now let me look at the e2e test to understand how local testing works:  

Read [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/test/e2e/e2e_test.go#1-1), lines 1 to 60

Now I have enough context. Let me update the README to document TLSRoute support: 

Replacing 35 lines with 55 lines in [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/README.md)


```

```
Now update the Features section to mention TLSRoute:  

Replacing 6 lines with 8 lines in [](file:///c%3A/Users/cbicchieri/Documents/workspace/att/cloudflare-kubernetes-gateway/README.md)


```

```
README is updated. Here's how to test locally before making a PR:

---

**Local testing options:**

1. **Unit tests** (no cluster needed):
   ```
   go test ./internal/controller/ -v
   ```

2. **Run the controller locally** against your current kubeconfig cluster (e.g. a Kind or minikube cluster with a real Cloudflare secret):
   ```
   # Install Gateway API CRDs (including experimental for TLSRoute)
   kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/experimental-install.yaml

   # Run controller against your cluster
   go run ./cmd/main.go
   ```
   Then apply a `GatewayClass`, `Gateway`, and `TLSRoute` resource to verify it reconciles.

3. **Full e2e test with Kind** (as the project's CI does):
   ```
   kind create cluster
   make test-e2e
   ```

4. **Quick manual validation** — to verify the TLSRoute controller wires up correctly with ArgoCD:
   ```bash
   # Create the TLSRoute
   kubectl apply -f - <<EOF
   apiVersion: gateway.networking.k8s.io/v1alpha2
   kind: TLSRoute
   metadata:
     name: argocd-server
     namespace: argocd
   spec:
     parentRefs:
     - name: gateway
       namespace: cloudflare-gateway
     hostnames:
     - argocd.example.com
     rules:
     - backendRefs:
       - name: argocd-server
         port: 443
   EOF
   ```
   Then check the controller logs for `Updated Tunnel configuration` with an `https://` ingress entry.

**Important:** You need a real Cloudflare account/token for the tunnel API calls to succeed. For pure code validation without Cloudflare, `go build att.` and unit tests are sufficient.

Made changes.