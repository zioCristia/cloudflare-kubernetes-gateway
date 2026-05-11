package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudflare/cloudflare-go/v2"
	"github.com/cloudflare/cloudflare-go/v2/dns"
	"github.com/cloudflare/cloudflare-go/v2/zero_trust"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// TLSRouteReconciler reconciles a TLSRoute object
type TLSRouteReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Namespace string
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=tlsroutes,verbs=list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo
func (r *TLSRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	target := &gatewayv1alpha2.TLSRoute{}
	gateways := []gatewayv1.Gateway{}
	hostnames := []gatewayv1.Hostname{}
	err := r.Get(ctx, req.NamespacedName, target)
	if err == nil {
		for _, parentRef := range target.Spec.ParentRefs {
			namespace := target.ObjectMeta.Namespace
			if parentRef.Namespace != nil {
				namespace = string(*parentRef.Namespace)
			}
			gateway := &gatewayv1.Gateway{}
			if err := r.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      string(parentRef.Name),
			}, gateway); err != nil {
				log.Error(err, "Failed to get Gateway")
				return ctrl.Result{}, err
			}
			gateways = append(gateways, *gateway)
		}

		hostnames = target.Spec.Hostnames
	} else {
		gatewayList := &gatewayv1.GatewayList{}
		if err := r.List(ctx, gatewayList); err != nil {
			log.Error(err, "Failed to list Gateways")
			return ctrl.Result{}, err
		}
		gateways = gatewayList.Items
	}

	routes := &gatewayv1alpha2.TLSRouteList{}
	if err := r.List(ctx, routes); err != nil {
		log.Error(err, "Failed to list TLSRoutes")
		return ctrl.Result{}, err
	}

	for _, gateway := range gateways {
		// check target is in scope
		gatewayClass := &gatewayv1.GatewayClass{}
		if err := r.Get(ctx, types.NamespacedName{
			Name: string(gateway.Spec.GatewayClassName),
		}, gatewayClass); err != nil {
			log.Error(err, "Failed to get GatewayClasses")
			return ctrl.Result{}, err
		}

		if gatewayClass.Spec.ControllerName != controllerName {
			continue
		}

		// search for sibling routes
		siblingRoutes := []gatewayv1alpha2.TLSRoute{}
		for _, searchRoute := range routes.Items {
			for _, searchParent := range searchRoute.Spec.ParentRefs {
				namespace := searchRoute.ObjectMeta.Namespace
				if searchParent.Namespace != nil {
					namespace = string(*searchParent.Namespace)
				}
				if namespace == gateway.Namespace && string(searchParent.Name) == gateway.Name {
					siblingRoutes = append(siblingRoutes, searchRoute)
					break
				}
			}
		}

		// fan out to siblings
		ingress := []zero_trust.TunnelConfigurationUpdateParamsConfigIngress{}
		for _, route := range siblingRoutes {
			for _, rule := range route.Spec.Rules {
				services := map[string]bool{}
				for _, backend := range rule.BackendRefs {
					if backend.Port == nil {
						err := errors.New("TLSRoute backend port is nil")
						log.Error(err, "TLSRoute backend port is required and nil", "backend", backend)
						continue
					}

					var namespace string
					if backend.Namespace == nil {
						namespace = route.Namespace
					} else {
						namespace = string(*backend.Namespace)
					}

					services[fmt.Sprintf("https://%s.%s:%d", string(backend.Name), namespace, int32(*backend.Port))] = true
				}

				// product of hostname and service
				for _, hostname := range route.Spec.Hostnames {
					for service := range services {
						ingress = append(ingress, zero_trust.TunnelConfigurationUpdateParamsConfigIngress{
							Hostname: cloudflare.String(string(hostname)),
							Service:  cloudflare.String(service),
						})
					}
				}
			}
		}

		// last rule must be the catch-all
		ingress = append(ingress, zero_trust.TunnelConfigurationUpdateParamsConfigIngress{
			Service: cloudflare.String("http_status:404"),
		})

		// increment AttachedRoutes in each gateway listener status
		gatewayObj := &gatewayv1.Gateway{}
		gatewayRef := types.NamespacedName{
			Namespace: gateway.Namespace,
			Name:      gateway.Name,
		}
		if err := r.Get(ctx, gatewayRef, gatewayObj); err != nil {
			log.Error(err, "Failed to re-fetch gateway")
			return ctrl.Result{}, err
		}
		listeners := []gatewayv1.ListenerStatus{}
		for _, listener := range gatewayObj.Status.Listeners {
			listener.AttachedRoutes = int32(len(ingress))
			listeners = append(listeners, listener)
		}
		log.Info("Updating Gateway listeners", "AttachedRoutes", len(ingress))
		gatewayObj.Status.Listeners = listeners
		if err := r.Status().Update(ctx, gatewayObj); err != nil {
			log.Error(err, "Failed to update Gateway status")
			return ctrl.Result{}, err
		}

		account, api, err := InitCloudflareApi(ctx, r.Client, string(gateway.Spec.GatewayClassName))
		if err != nil {
			log.Error(err, "Failed to initialize Cloudflare API")
			return ctrl.Result{}, err
		}

		tunnels, err := api.ZeroTrust.Tunnels.List(ctx, zero_trust.TunnelListParams{
			AccountID: cloudflare.String(account),
			IsDeleted: cloudflare.Bool(false),
			Name:      cloudflare.String(gateway.Name),
		})
		if err != nil {
			log.Error(err, "Failed to get tunnel from Cloudflare API")
			return ctrl.Result{}, err
		}
		if len(tunnels.Result) == 0 {
			log.Info("Tunnel doesn't exist yet, probably waiting for the Gateway controller. Retrying in 1 minute", "gateway", gateway.Name)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		tunnel := tunnels.Result[0]

		_, err = api.ZeroTrust.Tunnels.Configurations.Update(ctx, tunnel.ID, zero_trust.TunnelConfigurationUpdateParams{
			AccountID: cloudflare.String(account),
			Config: cloudflare.F[zero_trust.TunnelConfigurationUpdateParamsConfig](
				zero_trust.TunnelConfigurationUpdateParamsConfig{
					Ingress: cloudflare.F[[]zero_trust.TunnelConfigurationUpdateParamsConfigIngress](ingress),
				},
			),
		})
		if err != nil {
			log.Error(err, "Failed to update Tunnel configuration")
			return ctrl.Result{}, err
		}

		log.Info("Updated Tunnel configuration", "ingress", ingress)

		// duplicate CNAMEs can't exist, so the last parentRef wins
		for _, tlsHostname := range hostnames {
			hostname := string(tlsHostname)
			zoneID, err := FindZoneID(hostname, ctx, api, account)
			if err != nil {
				return ctrl.Result{}, err
			}

			content := fmt.Sprintf("%s.cfargotunnel.com", tunnel.ID)
			comment := "Managed by github.com/pl4nty/cloudflare-kubernetes-gateway"
			records, _ := api.DNS.Records.List(ctx, dns.RecordListParams{
				ZoneID:  cloudflare.String(zoneID),
				Proxied: cloudflare.Bool(true),
				Type:    cloudflare.F[dns.RecordListParamsType]("CNAME"),
				Name:    cloudflare.String(hostname),
			})
			if len(records.Result) == 0 {
				_, err := api.DNS.Records.New(ctx, dns.RecordNewParams{
					ZoneID: cloudflare.String(zoneID),
					Record: dns.CNAMERecordParam{
						Proxied: cloudflare.Bool(true),
						Type:    cloudflare.F[dns.CNAMERecordType]("CNAME"),
						Name:    cloudflare.String(hostname),
						Content: cloudflare.F[interface{}](content),
						Comment: cloudflare.String(comment),
					},
				})
				if err != nil {
					log.Error(err, "Failed to create DNS record", hostname, content)
					return ctrl.Result{}, err
				}
			} else {
				_, err := api.DNS.Records.Update(ctx, records.Result[0].ID, dns.RecordUpdateParams{
					ZoneID: cloudflare.String(zoneID),
					Record: dns.CNAMERecordParam{
						Proxied: cloudflare.Bool(true),
						Type:    cloudflare.F[dns.CNAMERecordType]("CNAME"),
						Name:    cloudflare.String(hostname),
						Content: cloudflare.F[interface{}](content),
						Comment: cloudflare.String(comment),
					},
				})
				if err != nil {
					log.Error(err, "Failed to update DNS record", hostname, content)
					return ctrl.Result{}, err
				}
			}
		}
		log.Info("Updated DNS records", "hostnames", hostnames)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TLSRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	pred := predicate.GenerationChangedPredicate{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha2.TLSRoute{}).
		WithEventFilter(pred).
		Complete(r)
}
