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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/apis/workloads/legacy"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
)

var _ = Describe("component deletion transformer", func() {
	const (
		clusterName = "missing-cluster"
		compName    = "mysql"
	)

	var (
		comp     *appsv1alpha1.Component
		transCtx *componentTransformContext
		graphCli model.GraphClient
		dag      *graph.DAG
	)

	ownerRef := func() []metav1.OwnerReference {
		controller := true
		return []metav1.OwnerReference{
			{
				APIVersion: appsv1alpha1.APIVersion,
				Kind:       appsv1alpha1.ComponentKind,
				Name:       comp.Name,
				UID:        comp.UID,
				Controller: &controller,
			},
		}
	}

	BeforeEach(func() {
		now := metav1.NewTime(time.Now())
		comp = &appsv1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         testCtx.DefaultNamespace,
				Name:              constant.GenerateClusterComponentName(clusterName, compName),
				UID:               types.UID("component-uid"),
				DeletionTimestamp: &now,
				Labels: map[string]string{
					constant.AppInstanceLabelKey:    clusterName,
					constant.KBAppComponentLabelKey: compName,
				},
			},
			Spec: appsv1alpha1.ComponentSpec{
				CompDef: "missing-compdef",
			},
			Status: appsv1alpha1.ComponentStatus{
				Phase: appsv1alpha1.DeletingClusterCompPhase,
			},
		}
	})

	It("deletes ownerReference dependents when parent cluster is missing", func() {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       comp.Namespace,
				Name:            comp.Name + "-env",
				OwnerReferences: ownerRef(),
				Finalizers:      []string{constant.DBComponentFinalizerName},
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       comp.Namespace,
				Name:            comp.Name + "-account-root",
				OwnerReferences: ownerRef(),
				Finalizers:      []string{constant.DBComponentFinalizerName},
			},
		}
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       comp.Namespace,
				Name:            comp.Name,
				OwnerReferences: ownerRef(),
				Finalizers:      []string{constant.DBComponentFinalizerName},
			},
		}
		pdb := &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       comp.Namespace,
				Name:            comp.Name,
				OwnerReferences: ownerRef(),
				Finalizers:      []string{constant.DBClusterFinalizerName},
			},
		}
		rsm := &legacy.ReplicatedStateMachine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       comp.Namespace,
				Name:            comp.Name,
				OwnerReferences: ownerRef(),
				Finalizers:      []string{constant.DBClusterFinalizerName, "rsm.workloads.kubeblocks.io/finalizer"},
			},
		}
		rsmCRD := &apiextv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "replicatedstatemachines.workloads.kubeblocks.io",
			},
			Spec: apiextv1.CustomResourceDefinitionSpec{
				Group: "workloads.kubeblocks.io",
				Names: apiextv1.CustomResourceDefinitionNames{
					Plural: "replicatedstatemachines",
					Kind:   "ReplicatedStateMachine",
				},
				Scope: apiextv1.NamespaceScoped,
				Versions: []apiextv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha1", Served: true, Storage: true},
				},
			},
		}
		unowned := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: comp.Namespace,
				Name:      "unowned",
			},
		}

		cli := fake.NewClientBuilder().
			WithScheme(rscheme).
			WithObjects(comp, cm, secret, svc, pdb, rsm, rsmCRD, unowned).
			Build()
		graphCli = model.NewGraphClient(cli)
		dag = graph.NewDAG()
		graphCli.Root(dag, comp, comp, model.ActionStatusPtr())
		transCtx = &componentTransformContext{
			Context:       ctx,
			Client:        graphCli,
			EventRecorder: nil,
			Logger:        logger,
			Component:     comp,
			ComponentOrig: comp.DeepCopy(),
		}

		err := (&componentDeletionTransformer{}).Transform(transCtx, dag)
		Expect(err).Should(HaveOccurred())
		Expect(err.Error()).Should(ContainSubstring("not all component sub-resources deleted"))

		Expect(graphCli.IsAction(dag, cm, model.ActionDeletePtr())).Should(BeTrue())
		Expect(graphCli.IsAction(dag, secret, model.ActionDeletePtr())).Should(BeTrue())
		Expect(graphCli.IsAction(dag, svc, model.ActionDeletePtr())).Should(BeTrue())
		Expect(graphCli.IsAction(dag, pdb, model.ActionDeletePtr())).Should(BeTrue())
		Expect(graphCli.IsAction(dag, rsm, model.ActionDeletePtr())).Should(BeTrue())
		Expect(graphCli.IsAction(dag, unowned, model.ActionDeletePtr())).Should(BeFalse())
	})
})
