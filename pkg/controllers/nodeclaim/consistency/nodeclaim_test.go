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

package consistency_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/test"
	. "github.com/aws/karpenter-core/pkg/test/expectations"
)

var _ = Describe("NodeClaimController", func() {
	var nodePool *v1beta1.NodePool

	BeforeEach(func() {
		nodePool = test.NodePool()
	})
	Context("Termination failure", func() {
		It("should detect issues with a node that is stuck deleting due to a PDB", func() {
			nodeClaim, node := test.NodeClaimAndNode(v1beta1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1beta1.NodePoolLabelKey:   nodePool.Name,
						v1.LabelInstanceTypeStable: "default-instance-type",
					},
				},
				Status: v1beta1.NodeClaimStatus{
					ProviderID: test.RandomProviderID(),
					Capacity: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("1"),
						v1.ResourceMemory: resource.MustParse("1Gi"),
						v1.ResourcePods:   resource.MustParse("10"),
					},
				},
			})
			podsLabels := map[string]string{"myapp": "deleteme"}
			pdb := test.PodDisruptionBudget(test.PDBOptions{
				Labels:         podsLabels,
				MaxUnavailable: &intstr.IntOrString{IntVal: 0, Type: intstr.Int},
			})
			nodeClaim.Finalizers = []string{"prevent.deletion/now"}
			p := test.Pod(test.PodOptions{ObjectMeta: metav1.ObjectMeta{Labels: podsLabels}})
			ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, p, pdb)
			ExpectManualBinding(ctx, env.Client, p, node)
			_ = env.Client.Delete(ctx, nodeClaim)
			ExpectReconcileSucceeded(ctx, nodeClaimConsistencyController, client.ObjectKeyFromObject(nodeClaim))
			Expect(recorder.DetectedEvent(fmt.Sprintf("can't drain node, PDB %s/%s is blocking evictions", pdb.Namespace, pdb.Name))).To(BeTrue())
		})
	})

	Context("Node Shape", func() {
		It("should detect issues that launch with much fewer resources than expected", func() {
			nodeClaim, node := test.NodeClaimAndNode(v1beta1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1beta1.NodePoolLabelKey:        nodePool.Name,
						v1.LabelInstanceTypeStable:      "arm-instance-type",
						v1beta1.NodeInitializedLabelKey: "true",
					},
				},
				Status: v1beta1.NodeClaimStatus{
					ProviderID: test.RandomProviderID(),
					Capacity: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("16"),
						v1.ResourceMemory: resource.MustParse("128Gi"),
						v1.ResourcePods:   resource.MustParse("10"),
					},
				},
			})
			node.Status.Capacity = v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("16"),
				v1.ResourceMemory: resource.MustParse("64Gi"),
				v1.ResourcePods:   resource.MustParse("10"),
			}
			ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
			ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)
			ExpectReconcileSucceeded(ctx, nodeClaimConsistencyController, client.ObjectKeyFromObject(nodeClaim))
			Expect(recorder.DetectedEvent("expected 128Gi of resource memory, but found 64Gi (50.0% of expected)")).To(BeTrue())
		})
	})
})
