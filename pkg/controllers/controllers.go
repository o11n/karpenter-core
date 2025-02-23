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

package controllers

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/deprovisioning"
	"github.com/aws/karpenter-core/pkg/controllers/leasegarbagecollection"
	metricsnode "github.com/aws/karpenter-core/pkg/controllers/metrics/node"
	metricspod "github.com/aws/karpenter-core/pkg/controllers/metrics/pod"
	metricsprovisioner "github.com/aws/karpenter-core/pkg/controllers/metrics/provisioner"
	nodeclaimconsistency "github.com/aws/karpenter-core/pkg/controllers/nodeclaim/consistency"
	nodeclaimdisruption "github.com/aws/karpenter-core/pkg/controllers/nodeclaim/disruption"
	nodeclaimgarbagecollection "github.com/aws/karpenter-core/pkg/controllers/nodeclaim/garbagecollection"
	nodeclaimlifecycle "github.com/aws/karpenter-core/pkg/controllers/nodeclaim/lifecycle"
	nodeclaimtermination "github.com/aws/karpenter-core/pkg/controllers/nodeclaim/termination"
	nodepoolcounter "github.com/aws/karpenter-core/pkg/controllers/nodepool/counter"
	nodepoolhash "github.com/aws/karpenter-core/pkg/controllers/nodepool/hash"
	"github.com/aws/karpenter-core/pkg/controllers/provisioning"
	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/controllers/state/informer"
	"github.com/aws/karpenter-core/pkg/controllers/termination"
	"github.com/aws/karpenter-core/pkg/controllers/termination/terminator"
	"github.com/aws/karpenter-core/pkg/events"
	"github.com/aws/karpenter-core/pkg/operator/controller"
)

func NewControllers(
	clock clock.Clock,
	kubeClient client.Client,
	kubernetesInterface kubernetes.Interface,
	cluster *state.Cluster,
	recorder events.Recorder,
	cloudProvider cloudprovider.CloudProvider,
) []controller.Controller {

	p := provisioning.NewProvisioner(kubeClient, kubernetesInterface.CoreV1(), recorder, cloudProvider, cluster)
	evictionQueue := terminator.NewQueue(kubernetesInterface.CoreV1(), recorder)

	return []controller.Controller{
		p, evictionQueue,
		deprovisioning.NewController(clock, kubeClient, p, cloudProvider, recorder, cluster),
		provisioning.NewController(kubeClient, p, recorder),
		nodepoolhash.NewProvisionerController(kubeClient),
		informer.NewDaemonSetController(kubeClient, cluster),
		informer.NewNodeController(kubeClient, cluster),
		informer.NewPodController(kubeClient, cluster),
		informer.NewProvisionerController(kubeClient, cluster),
		informer.NewMachineController(kubeClient, cluster),
		termination.NewController(kubeClient, cloudProvider, terminator.NewTerminator(clock, kubeClient, evictionQueue), recorder),
		metricspod.NewController(kubeClient),
		metricsprovisioner.NewController(kubeClient),
		metricsnode.NewController(cluster),
		nodepoolcounter.NewProvisionerController(kubeClient, cluster),
		nodeclaimconsistency.NewMachineController(clock, kubeClient, recorder, cloudProvider),
		nodeclaimlifecycle.NewMachineController(clock, kubeClient, cloudProvider, recorder),
		nodeclaimgarbagecollection.NewController(clock, kubeClient, cloudProvider),
		nodeclaimtermination.NewMachineController(kubeClient, cloudProvider),
		nodeclaimdisruption.NewMachineController(clock, kubeClient, cluster, cloudProvider),
		leasegarbagecollection.NewController(kubeClient),
	}
}
