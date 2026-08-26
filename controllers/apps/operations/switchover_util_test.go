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

package operations

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
	"github.com/apecloud/kubeblocks/pkg/generics"
	testapps "github.com/apecloud/kubeblocks/pkg/testutil/apps"
)

func TestValidateAutoFailover(t *testing.T) {
	const (
		namespace     = "default"
		clusterName   = "mongodb"
		componentName = "mongodb"
		oldPrimary    = "mongodb-0"
	)
	oldPrimaryUID := types.UID("old-primary-uid")
	notReadySince := metav1.NewTime(time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC))

	newPod := func(name string, uid types.UID) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
				Labels:    constant.GetComponentWellKnownLabels(clusterName, componentName),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: notReadySince,
				}},
			},
		}
	}
	newOpsRequest := func() *appsv1alpha1.OpsRequest {
		return &appsv1alpha1.OpsRequest{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				Annotations: map[string]string{
					AutoFailoverAnnotation:              AutoFailoverAnnotationValue,
					AutoFailoverOldPrimaryPodAnnotation: oldPrimary,
					AutoFailoverOldPrimaryUIDAnnotation: string(oldPrimaryUID),
					AutoFailoverNotReadySinceAnnotation: notReadySince.Format(time.RFC3339Nano),
				},
			},
			Spec: appsv1alpha1.OpsRequestSpec{
				TimeoutSeconds: pointer.Int32(AutoFailoverOpsTimeoutSeconds),
			},
		}
	}
	validate := func(
		t *testing.T,
		opsRequest *appsv1alpha1.OpsRequest,
		pods ...*corev1.Pod,
	) (bool, *corev1.Pod, error) {
		t.Helper()
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		objects := make([]client.Object, 0, len(pods))
		for _, pod := range pods {
			objects = append(objects, pod)
		}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		cluster := &appsv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: clusterName},
		}
		synthesized := &component.SynthesizedComponent{
			Name: componentName,
			Roles: []appsv1alpha1.ReplicaRole{{
				Name:        "primary",
				Serviceable: true,
				Writable:    true,
			}},
		}
		automatic, needed, primary, err := validateAutoFailover(
			context.Background(),
			cli,
			cluster,
			opsRequest,
			synthesized,
			&appsv1alpha1.Switchover{InstanceName: KBSwitchoverCandidateInstanceForAnyPod},
		)
		if !automatic {
			t.Fatal("validateAutoFailover() did not recognize the automatic request")
		}
		return needed, primary, err
	}

	t.Run("current primary remains eligible", func(t *testing.T) {
		pod := newPod(oldPrimary, oldPrimaryUID)
		pod.Labels[constant.RoleLabelKey] = "primary"
		needed, primary, err := validate(t, newOpsRequest(), pod)
		if err != nil || !needed || primary == nil || primary.Name != oldPrimary {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
	})

	t.Run("primary unknown after node loss remains eligible", func(t *testing.T) {
		pod := newPod(oldPrimary, oldPrimaryUID)
		pod.Labels[constant.RoleLabelKey] = "primary"
		pod.Status.Conditions[0].Status = corev1.ConditionUnknown
		pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now())
		needed, primary, err := validate(t, newOpsRequest(), pod)
		if err != nil || !needed || primary == nil || primary.Name != oldPrimary {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
	})

	t.Run("last known primary remains eligible after local role clear", func(t *testing.T) {
		pod := newPod(oldPrimary, oldPrimaryUID)
		pod.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		needed, primary, err := validate(t, newOpsRequest(), pod)
		if err != nil || !needed || primary == nil || primary.Name != oldPrimary {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
	})

	t.Run("new current primary waits until ready", func(t *testing.T) {
		oldPod := newPod(oldPrimary, oldPrimaryUID)
		oldPod.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		newPrimary := newPod("mongodb-1", types.UID("new-primary-uid"))
		newPrimary.Labels[constant.RoleLabelKey] = "primary"
		needed, primary, err := validate(t, newOpsRequest(), oldPod, newPrimary)
		if err == nil || needed || primary != nil {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
	})

	t.Run("new ready primary skips the old stepdown", func(t *testing.T) {
		oldPod := newPod(oldPrimary, oldPrimaryUID)
		oldPod.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		newPrimary := newPod("mongodb-1", types.UID("new-primary-uid"))
		newPrimary.Labels[constant.RoleLabelKey] = "primary"
		newPrimary.Status.Conditions[0].Status = corev1.ConditionTrue
		needed, primary, err := validate(t, newOpsRequest(), oldPod, newPrimary)
		if err != nil || needed || primary != nil {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
	})

	t.Run("ambiguous last known primaries wait", func(t *testing.T) {
		first := newPod(oldPrimary, oldPrimaryUID)
		first.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		second := newPod("mongodb-1", types.UID("second-uid"))
		second.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		needed, primary, err := validate(t, newOpsRequest(), first, second)
		if err == nil || needed || primary != nil {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
		if !intctrlutil.IsRequeueError(err) {
			t.Fatalf("validateAutoFailover() error = %T, want RequeueError", err)
		}
	})

	t.Run("stopped old primary waits for majority election", func(t *testing.T) {
		pod := newPod(oldPrimary, oldPrimaryUID)
		pod.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		pod.Status.Phase = corev1.PodFailed
		needed, primary, err := validate(t, newOpsRequest(), pod)
		if err == nil || needed || primary != nil {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
	})

	t.Run("creating deadline expires as a fatal error", func(t *testing.T) {
		opsRequest := newOpsRequest()
		opsRequest.Status.StartTimestamp = metav1.NewTime(
			time.Now().Add(-time.Duration(AutoFailoverOpsTimeoutSeconds+1) * time.Second),
		)
		pod := newPod(oldPrimary, oldPrimaryUID)
		pod.Annotations = map[string]string{constant.LastKnownRoleAnnotationKey: "primary"}
		pod.Status.Phase = corev1.PodFailed
		needed, primary, err := validate(t, opsRequest, pod)
		if err == nil || needed || primary != nil {
			t.Fatalf("validateAutoFailover() = needed %v, primary %v, err %v", needed, primary, err)
		}
		if !intctrlutil.IsTargetError(err, intctrlutil.ErrorTypeFatal) {
			t.Fatalf("validateAutoFailover() error = %T, want fatal error", err)
		}
	})
}

var _ = Describe("Switchover Util", func() {

	var (
		clusterName        = "test-cluster-repl"
		clusterDefName     = "test-cluster-def-repl"
		clusterVersionName = "test-cluster-version-repl"
	)

	var (
		clusterDefObj     *appsv1alpha1.ClusterDefinition
		clusterVersionObj *appsv1alpha1.ClusterVersion
		clusterObj        *appsv1alpha1.Cluster
	)

	defaultRole := func(index int32) string {
		role := constant.Secondary
		if index == 0 {
			role = constant.Primary
		}
		return role
	}

	cleanAll := func() {
		// must wait till resources deleted and no longer existed before the testcases start,
		// otherwise if later it needs to create some new resource objects with the same name,
		// in race conditions, it will find the existence of old objects, resulting failure to
		// create the new objects.
		By("clean resources")
		// delete cluster(and all dependent sub-resources), clusterversion and clusterdef
		testapps.ClearClusterResources(&testCtx)

		// clear rest resources
		inNS := client.InNamespace(testCtx.DefaultNamespace)
		ml := client.HasLabels{testCtx.TestObjLabelKey}
		// namespaced resources
		testapps.ClearResourcesWithRemoveFinalizerOption(&testCtx, generics.InstanceSetSignature, true, inNS, ml)
		testapps.ClearResources(&testCtx, generics.PodSignature, inNS, ml, client.GracePeriodSeconds(0))
		testapps.ClearResources(&testCtx, generics.OpsRequestSignature, inNS, ml)
	}

	BeforeEach(cleanAll)

	AfterEach(cleanAll)

	testNeedDoSwitchover := func() {
		By("Creating a cluster with replication workloadType.")
		clusterObj = testapps.NewClusterFactory(testCtx.DefaultNamespace, clusterName,
			clusterDefObj.Name, clusterVersionObj.Name).WithRandomName().
			AddComponent(testapps.DefaultRedisCompSpecName, testapps.DefaultRedisCompDefName).
			SetReplicas(testapps.DefaultReplicationReplicas).
			Create(&testCtx).GetObject()

		By("Creating a statefulSet of replication workloadType.")
		container := corev1.Container{
			Name:            "mock-redis-container",
			Image:           testapps.DefaultRedisImageName,
			ImagePullPolicy: corev1.PullIfNotPresent,
		}
		its := testapps.NewInstanceSetFactory(testCtx.DefaultNamespace,
			clusterObj.Name+"-"+testapps.DefaultRedisCompSpecName, clusterObj.Name, testapps.DefaultRedisCompSpecName).
			AddFinalizers([]string{constant.DBClusterFinalizerName}).
			AddContainer(container).
			AddAppInstanceLabel(clusterObj.Name).
			AddAppComponentLabel(testapps.DefaultRedisCompSpecName).
			AddAppManagedByLabel().
			SetReplicas(2).
			Create(&testCtx).GetObject()

		By("Creating Pods of replication workloadType.")
		for i := int32(0); i < *its.Spec.Replicas; i++ {
			_ = testapps.NewPodFactory(testCtx.DefaultNamespace, fmt.Sprintf("%s-%d", its.Name, i)).
				AddContainer(container).
				AddLabelsInMap(its.Labels).
				AddRoleLabel(defaultRole(i)).
				Create(&testCtx).GetObject()
		}

		opsSwitchover := &appsv1alpha1.Switchover{
			ComponentName: testapps.DefaultRedisCompSpecName,
			InstanceName:  fmt.Sprintf("%s-%s-%d", clusterObj.Name, testapps.DefaultRedisCompSpecName, 0),
		}

		reqCtx := intctrlutil.RequestCtx{
			Ctx: testCtx.Ctx,
		}
		compSpec := clusterObj.Spec.GetComponentByName(opsSwitchover.ComponentName)
		synthesizedComp, err := component.BuildSynthesizedComponentWrapper(reqCtx, k8sClient, clusterObj, compSpec)
		Expect(err).Should(Succeed())
		Expect(synthesizedComp).ShouldNot(BeNil())

		By("Test opsSwitchover.Instance is already primary, and do not need to do switchover.")
		needSwitchover, err := needDoSwitchover(testCtx.Ctx, k8sClient, clusterObj, synthesizedComp, opsSwitchover)
		Expect(err).Should(Succeed())
		Expect(needSwitchover).Should(BeFalse())

		By("Test opsSwitchover.Instance is not primary, and need to do switchover.")
		opsSwitchover.InstanceName = fmt.Sprintf("%s-%s-%d", clusterObj.Name, testapps.DefaultRedisCompSpecName, 1)
		needSwitchover, err = needDoSwitchover(testCtx.Ctx, k8sClient, clusterObj, synthesizedComp, opsSwitchover)
		Expect(err).Should(Succeed())
		Expect(needSwitchover).Should(BeTrue())

		By("Test opsSwitchover.Instance is *, and need to do switchover.")
		opsSwitchover.InstanceName = "*"
		needSwitchover, err = needDoSwitchover(testCtx.Ctx, k8sClient, clusterObj, synthesizedComp, opsSwitchover)
		Expect(err).Should(Succeed())
		Expect(needSwitchover).Should(BeTrue())
	}

	testDoSwitchover := func() {
		By("Creating a cluster with replication workloadType.")
		clusterObj = testapps.NewClusterFactory(testCtx.DefaultNamespace, clusterName,
			clusterDefObj.Name, clusterVersionObj.Name).WithRandomName().
			AddComponent(testapps.DefaultRedisCompSpecName, testapps.DefaultRedisCompDefName).
			SetReplicas(testapps.DefaultReplicationReplicas).
			Create(&testCtx).GetObject()

		By("Creating a statefulSet of replication workloadType.")
		container := corev1.Container{
			Name:            "mock-redis-container",
			Image:           testapps.DefaultRedisImageName,
			ImagePullPolicy: corev1.PullIfNotPresent,
		}
		its := testapps.NewInstanceSetFactory(testCtx.DefaultNamespace,
			clusterObj.Name+"-"+testapps.DefaultRedisCompSpecName, clusterObj.Name, testapps.DefaultRedisCompSpecName).
			AddFinalizers([]string{constant.DBClusterFinalizerName}).
			AddContainer(container).
			AddAppInstanceLabel(clusterObj.Name).
			AddAppComponentLabel(testapps.DefaultRedisCompSpecName).
			AddAppManagedByLabel().
			SetReplicas(2).
			Create(&testCtx).GetObject()

		By("Creating Pods of replication workloadType.")
		for i := int32(0); i < *its.Spec.Replicas; i++ {
			_ = testapps.NewPodFactory(testCtx.DefaultNamespace, fmt.Sprintf("%s-%d", its.Name, i)).
				AddContainer(container).
				AddLabelsInMap(its.Labels).
				AddRoleLabel(defaultRole(i)).
				Create(&testCtx).GetObject()
		}
		opsSwitchover := &appsv1alpha1.Switchover{
			ComponentName: testapps.DefaultRedisCompSpecName,
			InstanceName:  fmt.Sprintf("%s-%s-%d", clusterObj.Name, testapps.DefaultRedisCompSpecName, 1),
		}
		reqCtx := intctrlutil.RequestCtx{
			Ctx:      testCtx.Ctx,
			Recorder: k8sManager.GetEventRecorderFor("opsrequest-controller"),
		}
		By("Test create a job to do switchover")
		compSpec := clusterObj.Spec.GetComponentByName(opsSwitchover.ComponentName)
		synthesizedComp, err := component.BuildSynthesizedComponentWrapper(reqCtx, k8sClient, clusterObj, compSpec)
		Expect(err).Should(Succeed())
		Expect(synthesizedComp).ShouldNot(BeNil())
		opsRequest := testapps.NewOpsRequestObj("switchover-ops", testCtx.DefaultNamespace,
			clusterName, appsv1alpha1.SwitchoverType)
		opsRequest.Spec.SwitchoverList = []appsv1alpha1.Switchover{*opsSwitchover}
		testapps.CreateOpsRequest(ctx, testCtx, opsRequest)
		err = createSwitchoverJob(reqCtx, k8sClient, clusterObj, opsRequest, synthesizedComp, opsSwitchover)
		Expect(err).Should(Succeed())
	}

	// Scenarios
	Context("test switchover util", func() {
		BeforeEach(func() {
			By("Create a clusterDefinition obj with replication workloadType.")
			commandExecutorEnvItem := &appsv1alpha1.CommandExecutorEnvItem{
				Image: testapps.DefaultRedisImageName,
			}
			commandExecutorItem := &appsv1alpha1.CommandExecutorItem{
				Command: []string{"echo", "hello"},
				Args:    []string{},
			}
			scriptSpecSelectors := []appsv1alpha1.ScriptSpecSelector{
				{
					Name: "test-mock-cm",
				},
				{
					Name: "test-mock-cm-2",
				},
			}
			switchoverSpec := &appsv1alpha1.SwitchoverSpec{
				WithCandidate: &appsv1alpha1.SwitchoverAction{
					CmdExecutorConfig: &appsv1alpha1.CmdExecutorConfig{
						CommandExecutorEnvItem: *commandExecutorEnvItem,
						CommandExecutorItem:    *commandExecutorItem,
					},
					ScriptSpecSelectors: scriptSpecSelectors,
				},
				WithoutCandidate: &appsv1alpha1.SwitchoverAction{
					CmdExecutorConfig: &appsv1alpha1.CmdExecutorConfig{
						CommandExecutorEnvItem: *commandExecutorEnvItem,
						CommandExecutorItem:    *commandExecutorItem,
					},
					ScriptSpecSelectors: scriptSpecSelectors,
				},
			}
			clusterDefObj = testapps.NewClusterDefFactory(clusterDefName).
				AddComponentDef(testapps.ReplicationRedisComponent, testapps.DefaultRedisCompDefName).
				AddSwitchoverSpec(switchoverSpec).
				Create(&testCtx).GetObject()

			By("Create a clusterVersion obj with replication workloadType.")
			clusterVersionObj = testapps.NewClusterVersionFactory(clusterVersionName, clusterDefObj.GetName()).
				AddComponentVersion(testapps.DefaultRedisCompDefName).AddContainerShort(testapps.DefaultRedisContainerName, testapps.DefaultRedisImageName).
				Create(&testCtx).GetObject()

		})

		It("Test needDoSwitchover with different conditions", func() {
			testNeedDoSwitchover()
		})

		It("Test doSwitchover when opsRequest triggers", func() {
			testDoSwitchover()
		})
	})
})
