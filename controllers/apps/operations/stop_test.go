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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	opsutil "github.com/apecloud/kubeblocks/controllers/apps/operations/util"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
	"github.com/apecloud/kubeblocks/pkg/generics"
	testapps "github.com/apecloud/kubeblocks/pkg/testutil/apps"
	testk8s "github.com/apecloud/kubeblocks/pkg/testutil/k8s"
)

var _ = Describe("Stop OpsRequest", func() {
	var (
		randomStr             = testCtx.GetRandomStr()
		clusterDefinitionName = "cluster-definition-for-ops-" + randomStr
		clusterVersionName    = "clusterversion-for-ops-" + randomStr
		clusterName           = "cluster-for-ops-" + randomStr
		clusterDefName        = "test-clusterdef-" + randomStr
	)

	cleanEnv := func() {
		// must wait till resources deleted and no longer existed before the testcases start,
		// otherwise if later it needs to create some new resource objects with the same name,
		// in race conditions, it will find the existence of old objects, resulting failure to
		// create the new objects.
		By("clean resources")

		// delete cluster(and all dependent sub-resources), clusterversion and clusterdef
		testapps.ClearClusterResourcesWithRemoveFinalizerOption(&testCtx)

		// delete rest resources
		inNS := client.InNamespace(testCtx.DefaultNamespace)
		ml := client.HasLabels{testCtx.TestObjLabelKey}
		// namespaced
		testapps.ClearResourcesWithRemoveFinalizerOption(&testCtx, generics.InstanceSetSignature, true, inNS, ml)
		testapps.ClearResources(&testCtx, generics.OpsRequestSignature, inNS, ml)
		// default GracePeriod is 30s
		testapps.ClearResources(&testCtx, generics.PodSignature, inNS, ml, client.GracePeriodSeconds(0))
	}

	BeforeEach(cleanEnv)

	AfterEach(cleanEnv)

	Context("Test OpsRequest", func() {
		It("Test stop OpsRequest", func() {
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			testapps.MockInstanceSetComponent(&testCtx, clusterName, consensusComp)
			testapps.MockInstanceSetComponent(&testCtx, clusterName, statelessComp)
			testapps.MockInstanceSetComponent(&testCtx, clusterName, statefulComp)
			By("create 'Stop' opsRequest")
			createStopOpsRequest(opsRes)

			By("test top action and reconcile function")
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)
			// do stop cluster
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())
			for _, v := range opsRes.Cluster.Spec.ComponentSpecs {
				Expect(v.Stop).ShouldNot(BeNil())
				Expect(*v.Stop).Should(BeTrue())
			}
			_, err = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
			Expect(err == nil).Should(BeTrue())
		})

		It("Test stop specific components OpsRequest", func() {
			By("init operations resources with topology")
			opsRes, _, _ := initOperationsResourcesWithTopology(clusterDefName, clusterDefName, clusterName)
			pods := testapps.MockInstanceSetPods(&testCtx, nil, opsRes.Cluster, defaultCompName)

			By("create 'Stop' opsRequest for specific components")
			createStopOpsRequest(opsRes, defaultCompName)

			By("mock 'Stop' OpsRequest to Creating phase")
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)

			By("test stop action")
			stopHandler := StopOpsHandler{}
			err := stopHandler.Action(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("verify components are being stopped")
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, pobj *appsv1alpha1.Cluster) {
				for _, v := range pobj.Spec.ComponentSpecs {
					if v.Name == defaultCompName {
						Expect(v.Stop).ShouldNot(BeNil())
						Expect(*v.Stop).Should(BeTrue())
					} else {
						Expect(v.Stop).Should(BeNil())
					}
				}
			})).Should(Succeed())

			By("mock components stopped successfully")
			for i := range pods {
				testk8s.MockPodIsTerminating(ctx, testCtx, pods[i])
				testk8s.RemovePodFinalizer(ctx, testCtx, pods[i])
			}
			testapps.MockInstanceSetStatus(testCtx, opsRes.Cluster, defaultCompName)

			By("test reconcile")
			_, err = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("verify ops request completed")
			Eventually(testapps.GetOpsRequestPhase(&testCtx,
				client.ObjectKeyFromObject(opsRes.OpsRequest))).Should(Equal(appsv1alpha1.OpsSucceedPhase))
		})

		It("Test abort other running opsRequests", func() {
			By("init operations resources with topology")
			opsRes, _, _ := initOperationsResourcesWithTopology(clusterDefName, clusterDefName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create a 'Restart' opsRequest with intersection component")
			ops1 := createRestartOpsObj(clusterName, "restart-ops"+randomStr, defaultCompName)
			opsRes.OpsRequest = ops1
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)

			By("create a 'Restart' opsRequest with non-intersection component")
			ops2 := createRestartOpsObj(clusterName, "restart-ops2"+randomStr, secondaryCompName)
			ops2.Spec.Force = true
			opsRes.OpsRequest = ops2
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)

			By("create a 'Start' opsRequest")
			ops3 := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("start-ops-"+randomStr, testCtx.DefaultNamespace,
				clusterName, appsv1alpha1.StartType))
			opsRes.OpsRequest = ops3
			Expect(testapps.ChangeObjStatus(&testCtx, ops3, func() {
				ops3.Status.Phase = appsv1alpha1.OpsPendingPhase
			})).Should(Succeed())
			runAction(reqCtx, opsRes, appsv1alpha1.OpsPendingPhase)

			By("create 'Stop' opsRequest for all components")
			stopOps := createStopOpsRequest(opsRes, defaultCompName)
			opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(opsRes.Cluster)
			Expect(err).ShouldNot(HaveOccurred())
			opsSlice = append(opsSlice, appsv1alpha1.OpsRecorder{Name: stopOps.Name, Type: appsv1alpha1.StopType})
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, opsSlice)).Should(Succeed())
			stopHandler := StopOpsHandler{}
			err = stopHandler.Action(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("expect the 'Restart' opsRequest with intersection component to be Aborted")
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(ops1))).Should(Equal(appsv1alpha1.OpsAbortedPhase))

			By("expect the 'Restart' opsRequest with non-intersection component  to be Creating")
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(ops2))).Should(Equal(appsv1alpha1.OpsCreatingPhase))

			By("expect the 'Start' opsRequest to be Aborted")
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(ops3))).Should(Equal(appsv1alpha1.OpsAbortedPhase))
		})

		It("Test force stop OpsRequest bypasses running start OpsRequest", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create a running 'Start' opsRequest")
			startOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("start-ops-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.StartType))
			opsRes.OpsRequest = startOps
			Expect(testapps.ChangeObjStatus(&testCtx, startOps, func() {
				startOps.Status.Phase = appsv1alpha1.OpsPendingPhase
			})).Should(Succeed())
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)

			By("create force 'Stop' opsRequest while start holds the cluster queue")
			stopOps := createForceStopOpsRequest(opsRes)
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)

			By("expect force stop ops to bypass the start ops in queue")
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(opsSlice).Should(HaveLen(2))
				g.Expect(opsSlice[0].Name).Should(Equal(startOps.Name))
				g.Expect(opsSlice[0].InQueue).Should(BeFalse())
				g.Expect(opsSlice[1].Name).Should(Equal(stopOps.Name))
				g.Expect(opsSlice[1].InQueue).Should(BeFalse())
			})).Should(Succeed())

			By("execute stop action and expect start ops to be aborted")
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(startOps))).Should(Equal(appsv1alpha1.OpsAbortedPhase))
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				for _, comp := range cluster.Spec.ComponentSpecs {
					g.Expect(comp.Stop).ShouldNot(BeNil())
					g.Expect(*comp.Stop).Should(BeTrue())
				}
			})).Should(Succeed())
		})

		It("Test force stop OpsRequest waits for running horizontal scaling OpsRequest", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create a running 'HorizontalScaling' opsRequest in the cluster queue")
			hscaleOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("hscale-ops-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.HorizontalScalingType))
			Expect(testapps.ChangeObjStatus(&testCtx, hscaleOps, func() {
				hscaleOps.Status.Phase = appsv1alpha1.OpsRunningPhase
			})).Should(Succeed())
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{{
				Name: hscaleOps.Name,
				Type: appsv1alpha1.HorizontalScalingType,
			}})).Should(Succeed())
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				opsRes.Cluster = cluster
				opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(opsSlice).Should(HaveLen(1))
				g.Expect(opsSlice[0].InQueue).Should(BeFalse())
			})).Should(Succeed())

			By("create force 'Stop' opsRequest while horizontal scaling holds the cluster queue")
			stopOps := createForceStopOpsRequest(opsRes)
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("expect force stop ops to stay queued behind horizontal scaling")
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(opsSlice).Should(HaveLen(2))
				g.Expect(opsSlice[0].Name).Should(Equal(hscaleOps.Name))
				g.Expect(opsSlice[0].InQueue).Should(BeFalse())
				g.Expect(opsSlice[1].Name).Should(Equal(stopOps.Name))
				g.Expect(opsSlice[1].InQueue).Should(BeTrue())
			})).Should(Succeed())
		})

		It("Test force stop OpsRequest waits for queued non-start OpsRequest", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create a running 'Start' opsRequest and a queued 'HorizontalScaling' opsRequest")
			startOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("start-ops-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.StartType))
			hscaleOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("hscale-ops-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.HorizontalScalingType))
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{
				{
					Name: startOps.Name,
					Type: appsv1alpha1.StartType,
				},
				{
					Name:    hscaleOps.Name,
					Type:    appsv1alpha1.HorizontalScalingType,
					InQueue: true,
				},
			})).Should(Succeed())
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				opsRes.Cluster = cluster
				opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(opsSlice).Should(HaveLen(2))
				g.Expect(opsSlice[0].InQueue).Should(BeFalse())
				g.Expect(opsSlice[1].InQueue).Should(BeTrue())
			})).Should(Succeed())

			By("create force 'Stop' opsRequest while horizontal scaling is already queued before it")
			stopOps := createForceStopOpsRequest(opsRes)
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("expect force stop ops to stay queued behind the queued horizontal scaling ops")
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(opsSlice).Should(HaveLen(3))
				g.Expect(opsSlice[0].Name).Should(Equal(startOps.Name))
				g.Expect(opsSlice[0].InQueue).Should(BeFalse())
				g.Expect(opsSlice[1].Name).Should(Equal(hscaleOps.Name))
				g.Expect(opsSlice[1].InQueue).Should(BeTrue())
				g.Expect(opsSlice[2].Name).Should(Equal(stopOps.Name))
				g.Expect(opsSlice[2].InQueue).Should(BeTrue())
			})).Should(Succeed())
		})

		DescribeTable("Test force stop OpsRequest waits for other running OpsRequest",
			func(blockingOpsType appsv1alpha1.OpsType) {
				By("init operations resources")
				opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
				reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

				By("create a running opsRequest in the cluster queue")
				blockingOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("blocking-ops-"+testCtx.GetRandomStr(),
					testCtx.DefaultNamespace, clusterName, blockingOpsType))
				Expect(testapps.ChangeObjStatus(&testCtx, blockingOps, func() {
					blockingOps.Status.Phase = appsv1alpha1.OpsRunningPhase
				})).Should(Succeed())
				Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{{
					Name: blockingOps.Name,
					Type: blockingOpsType,
				}})).Should(Succeed())
				Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
					opsRes.Cluster = cluster
					opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
					g.Expect(err).ShouldNot(HaveOccurred())
					g.Expect(opsSlice).Should(HaveLen(1))
					g.Expect(opsSlice[0].InQueue).Should(BeFalse())
				})).Should(Succeed())

				By("create force 'Stop' opsRequest while the other opsRequest holds the cluster queue")
				stopOps := createForceStopOpsRequest(opsRes)
				_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
				Expect(err).ShouldNot(HaveOccurred())

				By("expect force stop ops to stay queued behind the other opsRequest")
				Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
					opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
					g.Expect(err).ShouldNot(HaveOccurred())
					g.Expect(opsSlice).Should(HaveLen(2))
					g.Expect(opsSlice[0].Name).Should(Equal(blockingOps.Name))
					g.Expect(opsSlice[0].InQueue).Should(BeFalse())
					g.Expect(opsSlice[1].Name).Should(Equal(stopOps.Name))
					g.Expect(opsSlice[1].InQueue).Should(BeTrue())
				})).Should(Succeed())
			},
			Entry("restart", appsv1alpha1.RestartType),
			Entry("vertical scaling", appsv1alpha1.VerticalScalingType),
		)

		It("Test force stop action does not abort horizontal scaling OpsRequest", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create a running 'HorizontalScaling' opsRequest")
			hscaleOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("hscale-ops-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.HorizontalScalingType))
			Expect(testapps.ChangeObjStatus(&testCtx, hscaleOps, func() {
				hscaleOps.Status.Phase = appsv1alpha1.OpsRunningPhase
			})).Should(Succeed())
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{{
				Name: hscaleOps.Name,
				Type: appsv1alpha1.HorizontalScalingType,
			}})).Should(Succeed())

			By("invoke force stop action directly")
			stopOps := createForceStopOpsRequest(opsRes)
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{
				{Name: hscaleOps.Name, Type: appsv1alpha1.HorizontalScalingType},
				{Name: stopOps.Name, Type: appsv1alpha1.StopType},
			})).Should(Succeed())
			Expect(StopOpsHandler{}.Action(reqCtx, k8sClient, opsRes)).Should(Succeed())

			By("expect horizontal scaling to remain running")
			Consistently(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(hscaleOps))).Should(Equal(appsv1alpha1.OpsRunningPhase))
		})

		It("Test force stop action does not abort a later start from a stale queue", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create force stop before the resume start OpsRequest")
			stopOps := createForceStopOpsRequest(opsRes)
			startOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("resume-start-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.StartType))
			Expect(testapps.ChangeObjStatus(&testCtx, startOps, func() {
				startOps.Status.Phase = appsv1alpha1.OpsPendingPhase
			})).Should(Succeed())
			stopOps.CreationTimestamp = startOps.CreationTimestamp

			By("simulate a stale cluster queue that contains the later start but not the current stop")
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{{
				Name: startOps.Name,
				Type: appsv1alpha1.StartType,
			}})).Should(Succeed())

			By("expect the stale queue to be retried without aborting the later start")
			Expect(StopOpsHandler{}.Action(reqCtx, k8sClient, opsRes)).Should(MatchError(ContainSubstring("missing from the cluster operations queue")))
			Consistently(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(startOps))).Should(Equal(appsv1alpha1.OpsPendingPhase))

			By("retry with a fresh queue and expect the later start to remain pending")
			Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, []appsv1alpha1.OpsRecorder{
				{Name: stopOps.Name, Type: appsv1alpha1.StopType},
				{Name: startOps.Name, Type: appsv1alpha1.StartType, InQueue: true},
			})).Should(Succeed())
			Expect(StopOpsHandler{}.Action(reqCtx, k8sClient, opsRes)).Should(Succeed())
			Consistently(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(startOps))).Should(Equal(appsv1alpha1.OpsPendingPhase))
		})

		It("Test specific component stop OpsRequest waits for running start OpsRequest", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}

			By("create a running 'Start' opsRequest")
			startOps := testapps.CreateOpsRequest(ctx, testCtx, testapps.NewOpsRequestObj("start-ops-"+randomStr,
				testCtx.DefaultNamespace, clusterName, appsv1alpha1.StartType))
			opsRes.OpsRequest = startOps
			Expect(testapps.ChangeObjStatus(&testCtx, startOps, func() {
				startOps.Status.Phase = appsv1alpha1.OpsPendingPhase
			})).Should(Succeed())
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)

			By("create component-level 'Stop' opsRequest while start holds the cluster queue")
			stopOps := createStopOpsRequest(opsRes, defaultCompName)
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("expect component-level stop ops to stay queued")
			Eventually(testapps.CheckObj(&testCtx, client.ObjectKeyFromObject(opsRes.Cluster), func(g Gomega, cluster *appsv1alpha1.Cluster) {
				opsSlice, err := opsutil.GetOpsRequestSliceFromCluster(cluster)
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(opsSlice).Should(HaveLen(2))
				g.Expect(opsSlice[0].Name).Should(Equal(startOps.Name))
				g.Expect(opsSlice[0].InQueue).Should(BeFalse())
				g.Expect(opsSlice[1].Name).Should(Equal(stopOps.Name))
				g.Expect(opsSlice[1].InQueue).Should(BeTrue())
			})).Should(Succeed())
		})

		It("Test stop OpsRequest is allowed when cluster is Creating", func() {
			By("init operations resources")
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			Expect(testapps.ChangeObjStatus(&testCtx, opsRes.Cluster, func() {
				opsRes.Cluster.Status.Phase = appsv1alpha1.CreatingClusterPhase
			})).Should(Succeed())

			By("create 'Stop' opsRequest")
			createStopOpsRequest(opsRes)

			By("expect cluster phase validation to pass")
			Expect(opsRes.OpsRequest.ValidateClusterPhase(opsRes.Cluster)).Should(Succeed())

			By("expect stop ops can enter creating phase")
			reqCtx := intctrlutil.RequestCtx{Ctx: ctx}
			runAction(reqCtx, opsRes, appsv1alpha1.OpsCreatingPhase)
		})
	})
})

func createStopOpsRequest(opsRes *OpsResource, stopCompNames ...string) *appsv1alpha1.OpsRequest {
	return createStopOpsRequestWithMutator(opsRes, nil, stopCompNames...)
}

func createForceStopOpsRequest(opsRes *OpsResource, stopCompNames ...string) *appsv1alpha1.OpsRequest {
	return createStopOpsRequestWithMutator(opsRes, func(ops *appsv1alpha1.OpsRequest) {
		ops.Spec.Force = true
	}, stopCompNames...)
}

func createStopOpsRequestWithMutator(opsRes *OpsResource, mutate func(*appsv1alpha1.OpsRequest), stopCompNames ...string) *appsv1alpha1.OpsRequest {
	By("create Stop opsRequest")
	ops := testapps.NewOpsRequestObj("stop-ops-"+testCtx.GetRandomStr(), testCtx.DefaultNamespace,
		opsRes.Cluster.Name, appsv1alpha1.StopType)
	var stopList []appsv1alpha1.ComponentOps
	for _, stopCompName := range stopCompNames {
		stopList = append(stopList, appsv1alpha1.ComponentOps{
			ComponentName: stopCompName,
		})
	}
	ops.Spec.StopList = stopList
	if mutate != nil {
		mutate(ops)
	}
	opsRes.OpsRequest = testapps.CreateOpsRequest(ctx, testCtx, ops)
	// set ops phase to Pending
	opsRes.OpsRequest.Status.Phase = appsv1alpha1.OpsPendingPhase
	return ops
}
