/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kube2store

import (
	"context"
	"time"

	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	proxystore "sigs.k8s.io/kpng/server/proxystore"
)

// K8sConfig is the data structure that users edit to influence
// the way that KPNG watches the K8s APIServer.
type K8sConfig struct {
	// UseSlices turns on endpoint slices.  This can go away eventually.
	UseSlices     bool

	// ServiceProxyName identifies a "different" service proxy, i.e. tells
	// KPNG we're not handling this service.
	ServiceProxyName string

	// Usually these are not specifically set by users...

	// ServiceLabelGlobs tells the proxy to filter certain services by label.
	ServiceLabelGlobs      []string
	// ServiceAnnotationGlobs tells the proxy to filter certain services by label.
	ServiceAnnonationGlobs []string

	NodeLabelGlobs      []string
	NodeAnnotationGlobs []string
}

// TODO: need to find a better home for this
const (
	// LabelServiceProxyName indicates that an alternative service
	// proxy will implement this Service.
	LabelServiceProxyName = "service.kubernetes.io/service-proxy-name"
)

func (c *K8sConfig) BindFlags(flags *pflag.FlagSet) {
	flags.StringVar(&c.ServiceProxyName, "service-proxy-name", "", "the "+LabelServiceProxyName+" match to use (handle normal services if not set)")

	flags.StringSliceVar(&c.ServiceLabelGlobs, "with-service-labels", nil, "service labels to include")
	flags.StringSliceVar(&c.ServiceAnnonationGlobs, "with-service-annotations", nil, "service annotations to include")

	flags.StringSliceVar(&c.NodeLabelGlobs, "with-node-labels", []string{
		"kubernetes.io/hostname", "topology.kubernetes.io/zone", "topology.kubernetes.io/region",
	}, "node labels to include")
	flags.StringSliceVar(&c.NodeAnnotationGlobs, "with-node-annotations", nil, "node annotations to include")
}

type Job struct {
	Kube   *kubernetes.Clientset
	Store  *proxystore.Store
	Config *K8sConfig
}

func (j Job) Run(ctx context.Context) {
	stopCh := ctx.Done()

	// start informers
	factory := informers.NewSharedInformerFactoryWithOptions(j.Kube, time.Second*30)
	factory.Start(stopCh)

	labelSelector := j.getLabelSelector().String()
	klog.Info("service label selector: ", labelSelector)
	svcFactory := informers.NewSharedInformerFactoryWithOptions(j.Kube, time.Second*30,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) { options.LabelSelector = labelSelector }))
	svcFactory.Start(stopCh)

	// start watches
	coreFactory := factory.Core().V1()

	servicesInformer := svcFactory.Core().V1().Services().Informer()
	servicesInformer.AddEventHandler(&serviceEventHandler{j.eventHandler(servicesInformer)})
	go servicesInformer.Run(stopCh)

	nodesInformer := coreFactory.Nodes().Informer()
	nodesInformer.AddEventHandler(&nodeEventHandler{j.eventHandler(nodesInformer)})
	go nodesInformer.Run(stopCh)

	slicesInformer := factory.Discovery().V1().EndpointSlices().Informer()
	slicesInformer.AddEventHandler(&sliceEventHandler{j.eventHandler(slicesInformer)})
	go slicesInformer.Run(stopCh)

	<-stopCh
	j.Store.Close()
}

func (j Job) eventHandler(informer cache.SharedIndexInformer) eventHandler {
	return eventHandler{
		k8sConfig: j.Config,
		s:         j.Store,
		informer:  informer,
	}
}

func (j Job) getLabelSelector() labels.Selector {
	labelSelector := labels.NewSelector()

	addReq := func(key string, op selection.Operator, v ...string) {
		req, err := labels.NewRequirement(key, op, v)
		if err != nil {
			klog.Exit(err)
		}

		labelSelector = labelSelector.Add(*req)
	}

	if proxyName := j.Config.ServiceProxyName; proxyName == "" {
		addReq(LabelServiceProxyName, selection.DoesNotExist)
	} else {
		addReq(LabelServiceProxyName, selection.Equals, proxyName)
	}

	addReq(v1.IsHeadlessService, selection.DoesNotExist)

	return labelSelector
}
