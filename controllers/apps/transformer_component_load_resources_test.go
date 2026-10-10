/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package apps

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
)

var _ = Describe("component load resources transformer", func() {
	const (
		clusterName = "missing-cluster"
		compName    = "mysql"
	)

	var (
		comp        *appsv1alpha1.Component
		transCtx    *componentTransformContext
		transformer *componentLoadResourcesTransformer
		nameSuffix  string
	)

	newComponent := func(labels map[string]string) *appsv1alpha1.Component {
		if labels == nil {
			labels = map[string]string{}
		}
		labels[constant.AppInstanceLabelKey] = clusterName
		return &appsv1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  testCtx.DefaultNamespace,
				Name:       fmt.Sprintf("%s-%s", constant.GenerateClusterComponentName(clusterName, compName), nameSuffix),
				Labels:     labels,
				Finalizers: []string{constant.DBComponentFinalizerName},
			},
			Spec: appsv1alpha1.ComponentSpec{
				CompDef:  "test-compdef",
				Replicas: 1,
			},
		}
	}

	runTransform := func() error {
		graphCli := model.NewGraphClient(&mockReader{})
		dag := graph.NewDAG()
		graphCli.Root(dag, comp, comp, model.ActionStatusPtr())
		transCtx = &componentTransformContext{
			Context:       ctx,
			Client:        graphCli,
			EventRecorder: nil,
			Logger:        logger,
			Component:     comp,
			ComponentOrig: comp.DeepCopy(),
		}
		transformer = &componentLoadResourcesTransformer{Client: k8sClient}
		return transformer.Transform(transCtx, dag)
	}

	BeforeEach(func() {
		comp = nil
		nameSuffix = rand.String(6)
	})

	AfterEach(func() {
		if comp != nil {
			fetched := &appsv1alpha1.Component{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(comp), fetched); err == nil {
				controllerutil.RemoveFinalizer(fetched, constant.DBComponentFinalizerName)
				_ = k8sClient.Update(ctx, fetched)
				_ = client.IgnoreNotFound(k8sClient.Delete(ctx, fetched))
			}
		}
	})

	expectDeletionTriggered := func() {
		Expect(k8sClient.Create(ctx, comp)).Should(Succeed())
		Expect(runTransform()).Should(HaveOccurred())

		fetched := &appsv1alpha1.Component{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: comp.Namespace,
			Name:      comp.Name,
		}, fetched)).Should(Succeed())
		Expect(fetched.DeletionTimestamp.IsZero()).Should(BeFalse())
	}

	It("deletes an orphaned component identified by ownerReference", func() {
		comp = newComponent(nil)
		comp.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: appsv1alpha1.APIVersion,
				Kind:       appsv1alpha1.ClusterKind,
				Name:       clusterName,
			},
		}

		expectDeletionTriggered()
	})

	It("deletes an orphaned component identified by cluster UID label", func() {
		comp = newComponent(map[string]string{
			constant.KBAppClusterUIDLabelKey: "missing-cluster-uid",
		})

		expectDeletionTriggered()
	})

	It("deletes an orphaned component identified by generated component labels", func() {
		comp = newComponent(map[string]string{
			constant.AppManagedByLabelKey:   constant.AppName,
			constant.KBAppComponentLabelKey: compName,
		})

		expectDeletionTriggered()
	})

	It("does not delete a component without KubeBlocks ownership metadata", func() {
		comp = newComponent(nil)

		Expect(k8sClient.Create(ctx, comp)).Should(Succeed())
		Expect(runTransform()).Should(HaveOccurred())

		fetched := &appsv1alpha1.Component{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: comp.Namespace,
			Name:      comp.Name,
		}, fetched)).Should(Succeed())
		Expect(fetched.DeletionTimestamp.IsZero()).Should(BeTrue())
	})
})
