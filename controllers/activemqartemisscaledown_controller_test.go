/*
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
// +kubebuilder:docs-gen:collapse=Apache License

/*
As usual, we start with the necessary imports. We also define some utility variables.
*/
package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	brokerv1beta1 "github.com/artemiscloud/activemq-artemis-operator/api/v1beta1"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/common"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/namer"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("Scale down controller", func() {

	BeforeEach(func() {
		BeforeEachSpec()
	})

	AfterEach(func() {
		AfterEachSpec()
	})

	Context("Scale down test", func() {
		It("deploy plan 2 clustered", Label("basic-scaledown-check"), func() {

			// note: we force a non local scaledown cr to exercise creds generation
			// hense only valid with DEPLOY_OPERATOR = false
			// 	see suite_test.go: os.Setenv("OPERATOR_WATCH_NAMESPACE", "SomeValueToCauesEqualitytoFailInIsLocalSoDrainControllerSortsCreds")
			if os.Getenv("USE_EXISTING_CLUSTER") == "true" {

				brokerName := NextSpecResourceName()
				ctx := context.Background()

				brokerCrd := generateOriginalArtemisSpec(defaultNamespace, brokerName)

				booleanTrue := true
				brokerCrd.Spec.DeploymentPlan.Clustered = &booleanTrue
				brokerCrd.Spec.DeploymentPlan.Size = common.Int32ToPtr(2)
				brokerCrd.Spec.DeploymentPlan.PersistenceEnabled = true
				// scale down is very sensitive to dns availability of ordinal 0
				brokerCrd.Spec.DeploymentPlan.ReadinessProbe = &corev1.Probe{
					InitialDelaySeconds: 2,
					TimeoutSeconds:      2,
					PeriodSeconds:       5,
					FailureThreshold:    5,
				}
				brokerCrd.Spec.DeploymentPlan.LivenessProbe = &corev1.Probe{
					InitialDelaySeconds: 2,
					TimeoutSeconds:      2,
					PeriodSeconds:       5,
					FailureThreshold:    5,
				}
				Expect(k8sClient.Create(ctx, brokerCrd)).Should(Succeed())

				createdBrokerCrd := &brokerv1beta1.ActiveMQArtemis{}
				getPersistedVersionedCrd(brokerCrd.ObjectMeta.Name, defaultNamespace, createdBrokerCrd)

				By("verifying two ready")
				Eventually(func(g Gomega) {

					getPersistedVersionedCrd(brokerCrd.ObjectMeta.Name, defaultNamespace, createdBrokerCrd)
					if verbose {
						fmt.Printf("\nSTATUS:%v\n", createdBrokerCrd.Status)
					}

					By("Check ready status")
					g.Expect(len(createdBrokerCrd.Status.PodStatus.Ready)).Should(BeEquivalentTo(2))
					g.Expect(meta.IsStatusConditionTrue(createdBrokerCrd.Status.Conditions, brokerv1beta1.DeployedConditionType)).Should(BeTrue())
					g.Expect(meta.IsStatusConditionTrue(createdBrokerCrd.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())

				}, existingClusterTimeout, interval).Should(Succeed())

				By("Sending a message to 1")
				podWithOrdinal := namer.CrToSS(brokerCrd.Name) + "-1"

				sendCmd := []string{"amq-broker/bin/artemis", "producer", "--user", "Jay", "--password", "activemq", "--url", "tcp://" + podWithOrdinal + ":61616", "--message-count", "1", "--destination", "queue://DLQ", "--verbose"}

				content, err := RunCommandInPod(podWithOrdinal, brokerName+"-container", sendCmd)
				Expect(err).To(BeNil())
				Expect(*content).Should(ContainSubstring("Produced: 1 messages"))

				By("Scaling down to ss-0")
				podWithOrdinal = namer.CrToSS(brokerCrd.Name) + "-0"

				Eventually(func(g Gomega) {

					getPersistedVersionedCrd(brokerCrd.ObjectMeta.Name, defaultNamespace, createdBrokerCrd)
					createdBrokerCrd.Spec.DeploymentPlan.Size = common.Int32ToPtr(1)
					// not checking return from update as it will error on repeat as there is no change
					// which is expected
					k8sClient.Update(ctx, createdBrokerCrd)
					By("checking scale down to 0 complete?")
					g.Expect(len(createdBrokerCrd.Status.PodStatus.Ready)).Should(BeEquivalentTo(1))
					// This moment a drainer pod will come up and do the message migration
					// so the pod number will change from 1 to 2 and back to 1.
					// checking message count on broker 0 to make sure scale down finally happens.
					By("Checking messsage count on broker 0")
					//./artemis queue stat --silent --url tcp://artemis-broker-ss-0:61616 --queueName DLQ
					queryCmd := []string{"amq-broker/bin/artemis", "queue", "stat", "--silent", "--url", "tcp://" + podWithOrdinal + ":61616", "--queueName", "DLQ"}
					stdout, err := RunCommandInPod(podWithOrdinal, brokerName+"-container", queryCmd)
					g.Expect(err).To(BeNil())
					if verbose {
						fmt.Printf("\nQSTAT_OUTPUT: %v\n", *stdout)
					}
					fields := strings.Split(*stdout, "|")
					g.Expect(fields[4]).To(Equal("MESSAGE_COUNT"), *stdout)
					g.Expect(strings.TrimSpace(fields[14])).To(Equal("1"), *stdout)

				}, existingClusterTimeout, interval*2).Should(Succeed())

				By("Receiving a message from 0")

				rcvCmd := []string{"amq-broker/bin/artemis", "consumer", "--user", "Jay", "--password", "activemq", "--url", "tcp://" + podWithOrdinal + ":61616", "--message-count", "1", "--destination", "queue://DLQ", "--receive-timeout", "10000", "--break-on-null", "--verbose"}
				content, err = RunCommandInPod(podWithOrdinal, brokerName+"-container", rcvCmd)

				Expect(err).To(BeNil())

				Expect(*content).Should(ContainSubstring("JMS Message ID:"))

				drainPod := &corev1.Pod{}
				drainPodKey := types.NamespacedName{Name: brokerName + "-ss-1", Namespace: defaultNamespace}
				By("verifying drain pod gone")
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, drainPodKey, drainPod)).ShouldNot(Succeed())
					By("drain pod gone")
				}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

				Expect(k8sClient.Delete(ctx, createdBrokerCrd)).Should(Succeed())
			}

		})
	})

	It("Toleration ok, verify scaledown", func() {

		// some required services on crc get evicted which invalidates this test of taints
		isOpenshift, err := common.DetectOpenshift()
		Expect(err).Should(BeNil())

		if !isOpenshift && os.Getenv("USE_EXISTING_CLUSTER") == "true" {

			By("Tainting the node with no schedule")
			Eventually(func(g Gomega) {

				// find our node, take the first one...
				nodes := &corev1.NodeList{}
				g.Expect(k8sClient.List(ctx, nodes, &client.ListOptions{})).Should(Succeed())
				g.Expect(len(nodes.Items) > 0).Should(BeTrue())

				node := nodes.Items[0]
				g.Expect(len(node.Spec.Taints)).Should(BeEquivalentTo(0))
				node.Spec.Taints = []corev1.Taint{{Key: "artemis", Value: "please", Effect: corev1.TaintEffectNoSchedule}}
				g.Expect(k8sClient.Update(ctx, &node)).Should(Succeed())
			}, timeout*2, interval).Should(Succeed())

			By("Creating a crd plan 2,clustered, with matching tolerations")
			ctx := context.Background()
			crd := generateArtemisSpec(defaultNamespace)
			clustered := true
			crd.Spec.DeploymentPlan.Clustered = &clustered
			crd.Spec.DeploymentPlan.Size = common.Int32ToPtr(2)
			crd.Spec.DeploymentPlan.PersistenceEnabled = true
			crd.Spec.DeploymentPlan.ReadinessProbe = &corev1.Probe{
				InitialDelaySeconds: 1,
				PeriodSeconds:       5,
			}

			crd.Spec.DeploymentPlan.Tolerations = []corev1.Toleration{
				{
					Key:      "artemis",
					Value:    "please",
					Operator: corev1.TolerationOpEqual,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			}

			By("Deploying the CRD " + crd.ObjectMeta.Name)
			Expect(k8sClient.Create(ctx, &crd)).Should(Succeed())

			brokerKey := types.NamespacedName{Name: crd.Name, Namespace: crd.Namespace}
			createdCrd := &brokerv1beta1.ActiveMQArtemis{}

			By("veryify pods started as matching taints/tolerations in play")
			Eventually(func(g Gomega) {

				g.Expect(k8sClient.Get(ctx, brokerKey, createdCrd)).Should(Succeed())
				g.Expect(len(createdCrd.Status.PodStatus.Ready)).Should(BeEquivalentTo(2))

			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			// need to produce and consume, even if scaledown controller is blocked, it can run once taints are removed
			podWithOrdinal := namer.CrToSS(brokerKey.Name) + "-1"
			By("Sending a message to Host: " + podWithOrdinal)

			sendCmd := []string{"amq-broker/bin/artemis", "producer", "--user", "Jay", "--password", "activemq", "--url", "tcp://" + podWithOrdinal + ":61616", "--message-count", "1"}
			content, err := RunCommandInPod(podWithOrdinal, brokerKey.Name+"-container", sendCmd)

			Expect(err).To(BeNil())

			Expect(*content).Should(ContainSubstring("Produced: 1 messages"))

			By("Scaling down to 1")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, brokerKey, createdCrd)).Should(Succeed())
				createdCrd.Spec.DeploymentPlan.Size = common.Int32ToPtr(1)
				g.Expect(k8sClient.Update(ctx, createdCrd)).Should(Succeed())
				By("Scale down to ss-0 update complete")
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("Checking ready 1")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, brokerKey, createdCrd)).Should(Succeed())
				g.Expect(len(createdCrd.Status.PodStatus.Ready)).Should(BeEquivalentTo(1))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			podWithOrdinal = namer.CrToSS(createdCrd.Name) + "-0"
			By("Receiving a message from Host: " + podWithOrdinal)

			recvCmd := []string{"amq-broker/bin/artemis", "consumer", "--user", "Jay", "--password", "activemq", "--url", "tcp://" + podWithOrdinal + ":61616", "--message-count", "1", "--receive-timeout", "10000", "--break-on-null", "--verbose"}

			content, err = RunCommandInPod(podWithOrdinal, brokerKey.Name+"-container", recvCmd)

			Expect(err).To(BeNil())

			Expect(*content).Should(ContainSubstring("JMS Message ID:"))

			By("reverting taints on node")
			Eventually(func(g Gomega) {

				// find our node, take the first one...
				nodes := &corev1.NodeList{}
				g.Expect(k8sClient.List(ctx, nodes, &client.ListOptions{})).Should(Succeed())
				g.Expect(len(nodes.Items) > 0).Should(BeTrue())

				node := nodes.Items[0]
				g.Expect(len(node.Spec.Taints)).Should(BeEquivalentTo(1))
				node.Spec.Taints = []corev1.Taint{}
				g.Expect(k8sClient.Update(ctx, &node)).Should(Succeed())
			}, timeout*2, interval).Should(Succeed())

			By("accessing drain pod")
			drainPod := &corev1.Pod{}
			drainPodKey := types.NamespacedName{Name: brokerKey.Name + "-ss-1", Namespace: defaultNamespace}
			By("flipping MessageMigration to release drain pod CR, and PVC")
			Expect(k8sClient.Get(ctx, drainPodKey, drainPod)).Should(Succeed())

			Eventually(func(g Gomega) {

				g.Expect(k8sClient.Get(ctx, brokerKey, createdCrd)).Should(Succeed())
				By("flipping message migration state (from default true) on brokerCr")
				booleanFalse := false
				createdCrd.Spec.DeploymentPlan.MessageMigration = &booleanFalse
				g.Expect(k8sClient.Update(ctx, createdCrd)).Should(Succeed())
				By("Unset message migration in broker cr")

			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying drain pod gone")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, drainPodKey, drainPod)).ShouldNot(Succeed())
				By("drain pod gone")
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, createdCrd)).Should(Succeed())
		}
	})

	It("scaledown to the right pod", Label("scale-down-target-selection"), func() {

		if os.Getenv("USE_EXISTING_CLUSTER") == "true" {

			brokerName := NextSpecResourceName()

			brokerCrd, createdBrokerCrd := DeployCustomBroker(defaultNamespace, func(candidate *brokerv1beta1.ActiveMQArtemis) {
				candidate.Name = brokerName
				candidate.Spec.DeploymentPlan.Size = common.Int32ToPtr(3)
				candidate.Spec.DeploymentPlan.PersistenceEnabled = true

				candidate.Spec.DeploymentPlan.ReadinessProbe = &corev1.Probe{
					InitialDelaySeconds: 2,
					TimeoutSeconds:      2,
					PeriodSeconds:       5,
					FailureThreshold:    5,
				}
				candidate.Spec.DeploymentPlan.LivenessProbe = &corev1.Probe{
					InitialDelaySeconds: 2,
					TimeoutSeconds:      2,
					PeriodSeconds:       5,
					FailureThreshold:    5,
				}
			})

			By("verifying pods ready")
			Eventually(func(g Gomega) {

				getPersistedVersionedCrd(brokerCrd.ObjectMeta.Name, defaultNamespace, createdBrokerCrd)
				By("Check ready status")
				g.Expect(len(createdBrokerCrd.Status.PodStatus.Ready)).Should(BeEquivalentTo(3))
				g.Expect(meta.IsStatusConditionTrue(createdBrokerCrd.Status.Conditions, brokerv1beta1.DeployedConditionType)).Should(BeTrue())
				g.Expect(meta.IsStatusConditionTrue(createdBrokerCrd.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())

			}, existingClusterTimeout, interval).Should(Succeed())

			By("send 2 messages to DLQ in the 3rd pod")
			podWithOrdinal2 := namer.CrToSS(brokerName) + "-2"
			By("sending a message to Host: " + podWithOrdinal2)

			sendCmd := []string{"amq-broker/bin/artemis", "producer", "--user", "Jay", "--password", "activemq", "--url", "tcp://" + podWithOrdinal2 + ":61616", "--message-count", "2", "--destination", "DLQ"}
			content, err := RunCommandInPod(podWithOrdinal2, brokerName+"-container", sendCmd)

			Expect(err).To(BeNil())
			Expect(*content).Should(ContainSubstring("Produced: 2 messages"))

			By("block DLQ on pod0 to prevent scaledown")
			podWithOrdinal0 := namer.CrToSS(brokerCrd.Name) + "-0"
			Eventually(func(g Gomega) {
				curlUrl := "http://" + podWithOrdinal0 + ":8161/console/jolokia/exec/org.apache.activemq.artemis:address=\"DLQ\",broker=\"amq-broker\",component=addresses/block()"

				blockCmd := []string{"curl", "-k", "-H", "Origin: http://localhost:8161", "-u", "user:password", curlUrl}
				reply, err := RunCommandInPod(podWithOrdinal0, brokerName+"-container", blockCmd)
				g.Expect(err).To(BeNil())
				g.Expect(*reply).To(ContainSubstring("\"value\":true"))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("scale down to 2")
			brokerKey := types.NamespacedName{Name: brokerName, Namespace: defaultNamespace}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, brokerKey, createdBrokerCrd)).Should(Succeed())
				createdBrokerCrd.Spec.DeploymentPlan.Size = common.Int32ToPtr(2)
				g.Expect(k8sClient.Update(ctx, createdBrokerCrd)).Should(Succeed())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("checking ready 2")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, brokerKey, createdBrokerCrd)).Should(Succeed())
				g.Expect(len(createdBrokerCrd.Status.PodStatus.Ready)).Should(BeEquivalentTo(2))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("Checking no messages on pod 0")
			Eventually(func(g Gomega) {
				curlUrl := "http://" + podWithOrdinal0 + ":8161/console/jolokia/read/org.apache.activemq.artemis:address=\"DLQ\",broker=\"amq-broker\",component=addresses/MessageCount"

				blockCmd := []string{"curl", "-s", "-k", "-H", "Origin: http://localhost:8161", "-u", "user:password", curlUrl}
				result, err := RunCommandInPod(podWithOrdinal0, brokerName+"-container", blockCmd)
				g.Expect(err).To(BeNil())

				g.Expect(*result).To(ContainSubstring("\"value\":0"))

			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			podWithOrdinal1 := namer.CrToSS(createdBrokerCrd.Name) + "-1"
			By("Checking 2 messages on pod 1")
			Eventually(func(g Gomega) {
				curlUrl := "http://" + podWithOrdinal1 + ":8161/console/jolokia/read/org.apache.activemq.artemis:address=\"DLQ\",broker=\"amq-broker\",component=addresses/MessageCount"

				blockCmd := []string{"curl", "-s", "-k", "-H", "Origin: http://localhost:8161", "-u", "user:password", curlUrl}
				result, err := RunCommandInPod(podWithOrdinal1, brokerName+"-container", blockCmd)
				g.Expect(err).To(BeNil())

				g.Expect(*result).To(ContainSubstring("\"value\":2"))

			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("Receiving a message from Host: " + podWithOrdinal1)
			recvCmd := []string{"amq-broker/bin/artemis", "consumer", "--user", "Jay", "--password", "activemq", "--url", "tcp://" + podWithOrdinal1 + ":61616", "--message-count", "2", "--destination", "DLQ", "--receive-timeout", "10000"}
			content, err = RunCommandInPod(podWithOrdinal1, brokerKey.Name+"-container", recvCmd)

			Expect(err).To(BeNil())
			Expect(*content).Should(ContainSubstring("Consumed: 2 messages"))
			Expect(*content).ShouldNot(ContainSubstring("Consumed: 0 messages"))

			CleanResource(createdBrokerCrd, createdBrokerCrd.Name, createdBrokerCrd.Namespace)
		}
	})

})
