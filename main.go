// Copyright 2023 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/exp/slices"
	cloudbilling "google.golang.org/api/cloudbilling/v1"
	container "google.golang.org/api/container/v1"
	"google.golang.org/api/option"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

const SERVICE_AUTOPILOT = "CCD8-9BF1-090E"
const CLUSTER_FEE = 0.1
const ARM_TYPE_PREFIX = "t2a-"

// Committed use discounts for Autopilot clusters are available.
// With committed use discounts, you will receive 45% discount off on-demand
// pricing for a three-year commitment or 20% discount off on-demand
// pricing for a one-year commitment.

const ONE_YEAR_DISCOUNT = 0.8
const THREE_YEAR_DISCOUNT = 0.55

type ap_pricing struct {
	// generic for all
	region        string
	storage_price float64

	// regular pricing
	cpu_price                 float64
	memory_price              float64
	cpu_balanced_price        float64
	memory_balanced_price     float64
	cpu_scaleout_price        float64
	memory_scaleout_price     float64
	cpu_arm_scaleout_price    float64
	memory_arm_scaleout_price float64

	// spot pricing
	spot_cpu_price                 float64
	spot_memory_price              float64
	spot_cpu_balanced_price        float64
	spot_memory_balanced_price     float64
	spot_cpu_scaleout_price        float64
	spot_memory_scaleout_price     float64
	spot_arm_cpu_scaleout_price    float64
	spot_arm_memory_scaleout_price float64
}

func main() {
	quietFlag := flag.Bool("quiet", false, "Generate no output to stdout")
	jsonFlag := flag.Bool("json", false, "Generate json file with the results")
	jsonFileFlag := flag.String("json-file", "./output.json", "json file location")
	flag.Parse()

	info_style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("225")).Background(lipgloss.Color("128"))
	node_style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("225")).Background(lipgloss.Color("160"))
	workload_style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("25")).Background(lipgloss.Color("192"))

	// Setting up kube configurations
	kubeConfig, kubeConfigPath, err := GetKubeConfig()
	if err != nil {
		log.Fatalf("Error getting kubernetes config: %v\n", err)
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("Error setting kubernetes config: %v\n", err)
	}

	mclientset, err := metricsv.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("Error setting kubernetes metrics config: %v\n", err)
	}

	svc, err := container.NewService(context.Background())
	if err != nil {
		log.Fatalf("Error initializing GKE client: %v", err)
	}

	// Extract the information out of kube config file
	currentContext, err := GetCurrentContext(kubeConfigPath)
	if err != nil {
		log.Fatalf("Error getting GKE context: %v", err)
	}

	clusterName := currentContext[3]
	clusterZone := currentContext[2]
	clusterProject := currentContext[1]
	clusterRegion := strings.Join(
		strings.Split(clusterZone, "-")[:len(
			strings.Split(
				clusterZone,
				"-",
			),
		)-1],
		"-",
	)
	clusterLocation := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", clusterProject, clusterZone, clusterName)

	cluster, err := svc.Projects.Locations.Clusters.Get(clusterLocation).Do()
	if err != nil {
		log.Fatalf("Error getting GKE cluster information: %s, %v", clusterName, err)
	}

	if cluster.Autopilot.Enabled {
		log.Fatalf("This is already an Autopilot cluster, `aborting`")
	}

	pricing, err := GetAutopilotPricing(clusterRegion)
	if err != nil {
		log.Fatalf("Error getting cloud pricing: %v", err)
	}

	nodes, err := GetClusterNodes(clientset)
	if err != nil {
		log.Fatalf("Error getting cluster nodes: %v", err)
	}

	workloads, err := PopulateWorkloads(clientset, mclientset, nodes, pricing)
	if err != nil {
		log.Fatalf(err.Error())
	}

	if !*quietFlag {
		fmt.Println(info_style.Render(fmt.Sprintf("Cluster %q (%s) on version: v%s", cluster.Name, cluster.Status, cluster.CurrentMasterVersion)))
		fmt.Println()

		fmt.Println(node_style.Render(fmt.Sprintf("Total nodes at %s: %d", clusterRegion, len(nodes))))
		DisplayNodeTable(nodes)

		fmt.Println(workload_style.Render(fmt.Sprintf("\nTotal workloads on %s: %d", clusterName, len(workloads))))
		DisplayWorkloadTable(nodes)
	}

	if *jsonFlag {
		jsonOutput, err := os.Create(*jsonFileFlag)
		if err != nil {
			log.Fatalf("Error creating file for json output: %s", err.Error())
		}

		file, _ := json.MarshalIndent(nodes, "", " ")
		_, err = jsonOutput.Write(file)
		if err != nil {
			log.Printf("Error writing json to file: %s", err.Error())
		}
	}
}

func PopulateWorkloads(clientset *kubernetes.Clientset, mclientset *metricsv.Clientset, nodes map[string]Node, pricing ap_pricing) ([]Workload, error) {
	podMetricsList, err := mclientset.MetricsV1beta1().PodMetricses("").List(context.TODO(), metav1.ListOptions{FieldSelector: "metadata.namespace!=kube-system,metadata.namespace!=gke-gmp-system"})
	var workloads []Workload

	if err != nil {
		log.Fatalf(err.Error())
	}

	for _, v := range podMetricsList.Items {
		pod, err := DescribePod(clientset, v.Name, v.Namespace)
		if err != nil {
			return nil, err
		}

		if err != nil {
			return nil, err
		}

		var cpu_m int64 = 0
		var memory_m int64 = 0
		var storage_m int64 = 0
		containers_in_pod := 0

		// Sum used resources from the Pod
		for _, k := range v.Containers {
			cpu_m += k.Usage.Cpu().MilliValue()
			memory_m += k.Usage.Memory().MilliValue() / 1000000000            // Division to get MiB
			storage_m += k.Usage.StorageEphemeral().MilliValue() / 1000000000 // Division to get MiB
			containers_in_pod++
		}

		// Check and modify the limits of summed workloads from the Pod
		cpu_m, memory_m, storage_m = ValidateLowerLimits(cpu_m, memory_m, storage_m)

		compute_class := DecideComputeClass(v.Name, cpu_m, memory_m, strings.Contains(nodes[pod.Spec.NodeName].InstanceType, ARM_TYPE_PREFIX))

		cost := CalculatePricing(cpu_m, memory_m, storage_m, pricing, compute_class, nodes[pod.Spec.NodeName].Spot)

		workload_object := Workload{
			Name:          v.Name,
			Containers:    containers_in_pod,
			Node_name:     pod.Spec.NodeName,
			Cpu:           cpu_m,
			Memory:        memory_m,
			Storage:       storage_m,
			Cost:          cost,
			Compute_class: compute_class,
		}

		workloads = append(workloads, workload_object)

		if entry, ok := nodes[pod.Spec.NodeName]; ok {
			entry.Workloads = append(entry.Workloads, workload_object)
			entry.Cost += cost
			nodes[pod.Spec.NodeName] = entry
		}

	}

	return workloads, nil

}

func CalculatePricing(cpu_m int64, memory_m int64, storage_m int64, pricing ap_pricing, class ComputeClass, spot bool) float64 {
	// If spot, calculations are done based on spot pricing
	if spot {
		switch class {
		case COMPUTE_CLASS_BALANCED:
			return pricing.spot_cpu_price*float64(cpu_m)/1000 + pricing.spot_memory_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
		case COMPUTE_CLASS_SCALEOUT:
			return pricing.spot_cpu_scaleout_price*float64(cpu_m)/1000 + pricing.spot_memory_scaleout_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
		case COMPUTE_CLASS_SCALEOUT_ARM:
			if pricing.spot_arm_cpu_scaleout_price == 0 || pricing.spot_arm_memory_scaleout_price == 0 {
				log.Printf("ARM pricing is not available in this %s region.", pricing.region)
			}
			return pricing.spot_arm_cpu_scaleout_price*float64(cpu_m)/1000 + pricing.spot_arm_memory_scaleout_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
		default:
			return pricing.spot_cpu_price*float64(cpu_m)/1000 + pricing.spot_memory_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
		}
	}

	switch class {
	case COMPUTE_CLASS_BALANCED:
		return pricing.cpu_balanced_price*float64(cpu_m)/1000 + pricing.memory_balanced_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
	case COMPUTE_CLASS_SCALEOUT:
		return pricing.cpu_scaleout_price*float64(cpu_m)/1000 + pricing.memory_scaleout_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
	case COMPUTE_CLASS_SCALEOUT_ARM:
		if pricing.cpu_arm_scaleout_price == 0 || pricing.memory_arm_scaleout_price == 0 {
			log.Printf("ARM pricing is not available in this %s region.", pricing.region)
		}
		return pricing.cpu_arm_scaleout_price*float64(cpu_m)/1000 + pricing.memory_arm_scaleout_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
	default:
		return pricing.cpu_price*float64(cpu_m)/1000 + pricing.memory_price*float64(memory_m)/1000 + pricing.storage_price*float64(storage_m)/1000
	}
}

func DecideComputeClass(workload string, mCPU int64, memory int64, arm64 bool) ComputeClass {
	ratio := math.Ceil(float64(memory) / float64(mCPU))

	// ARM64 is still experimental
	if arm64 {
		if ratio != 4 || mCPU > 43000 || memory > 172000 {
			log.Printf("Requesting arm64 but requested mCPU () or memory or ratio are out of accepted range(%s).\n", workload)
		}

		return COMPUTE_CLASS_SCALEOUT_ARM
	}

	// For T2a machines, default to scale-out compute class, since it's the only one supporting it
	if ratio >= 1 && ratio <= 6.5 && mCPU <= 30000 && memory <= 110000 {
		return COMPUTE_CLASS_REGULAR
	}

	// If we are out of Regular range, suggest Scale-Out
	if ratio == 4 && mCPU <= 54000 && memory <= 216000 {
		return COMPUTE_CLASS_SCALEOUT
	}

	// If usage is more than general-purpose limits, default to balanced
	if ratio >= 1 && ratio <= 8 && (mCPU > 30000 || memory > 110000) {
		return COMPUTE_CLASS_BALANCED
	}

	log.Printf("Couldn't find a matching compute class for %s. Defaulting to 'Regular'. Please check manually.\n", workload)

	return COMPUTE_CLASS_REGULAR
}

func ValidateLowerLimits(mCPU int64, memory int64, storage int64) (int64, int64, int64) {
	if mCPU < 250 {
		mCPU = 250
	}

	if memory < 500 {
		memory = 500
	}

	if storage < 10 {
		storage = 10
	}

	return mCPU, memory, storage
}

func GetAutopilotPricing(region string) (ap_pricing, error) {
	// Init all to zeroes
	pricing := ap_pricing{
		region:                         region,
		storage_price:                  0,
		cpu_price:                      0,
		memory_price:                   0,
		cpu_balanced_price:             0,
		memory_balanced_price:          0,
		cpu_scaleout_price:             0,
		memory_scaleout_price:          0,
		cpu_arm_scaleout_price:         0,
		memory_arm_scaleout_price:      0,
		spot_cpu_price:                 0,
		spot_memory_price:              0,
		spot_cpu_balanced_price:        0,
		spot_memory_balanced_price:     0,
		spot_cpu_scaleout_price:        0,
		spot_memory_scaleout_price:     0,
		spot_arm_cpu_scaleout_price:    0,
		spot_arm_memory_scaleout_price: 0,
	}

	ctx := context.Background()

	cloudbillingService, err := cloudbilling.NewService(ctx, option.WithScopes(cloudbilling.CloudPlatformScope))
	if err != nil {
		err = fmt.Errorf("unable to initialize cloud billing service: %v", err)
		return ap_pricing{}, err
	}

	pricingInfo, err := cloudbillingService.Services.Skus.List("services/" + SERVICE_AUTOPILOT).CurrencyCode("USD").Do()
	if err != nil {
		err = fmt.Errorf("unable to fetch cloud billing prices: %v", err)
		return ap_pricing{}, err
	}

	for _, sku := range pricingInfo.Skus {
		if !slices.Contains(sku.ServiceRegions, region) {
			continue
		}

		decimal := sku.PricingInfo[0].PricingExpression.TieredRates[0].UnitPrice.Units * 1000000000
		mantissa := sku.PricingInfo[0].PricingExpression.TieredRates[0].UnitPrice.Nanos * int64(sku.PricingInfo[0].PricingExpression.DisplayQuantity)

		price := float64(decimal+mantissa) / 1000000000

		switch sku.Description {
		case "Autopilot Pod Ephemeral Storage Requests (" + region + ")":
			pricing.storage_price = price

		case "Autopilot Pod Memory Requests (" + region + ")":
			pricing.memory_price = price

		case "Autopilot Pod mCPU Requests (" + region + ")":
			pricing.cpu_price = price

		case "Autopilot Balanced Pod Memory Requests (" + region + ")":
			pricing.memory_balanced_price = price

		case "Autopilot Balanced Pod mCPU Requests (" + region + ")":
			pricing.cpu_balanced_price = price

		case "Autopilot Scale-Out x86 Pod Memory Requests (" + region + ")":
			pricing.memory_scaleout_price = price

		case "Autopilot Scale-Out x86 Pod mCPU Requests (" + region + ")":
			pricing.cpu_scaleout_price = price

		case "Autopilot Scale-Out Arm Spot Pod Memory Requests (" + region + ")":
			pricing.memory_arm_scaleout_price = price

		case "Autopilot Scale-Out Arm Spot Pod mCPU Requests (" + region + ")":
			pricing.cpu_arm_scaleout_price = price

		case "Autopilot Spot Pod Memory Requests (" + region + ")":
			pricing.spot_memory_price = price

		case "Autopilot Spot Pod mCPU Requests (" + region + ")":
			pricing.spot_cpu_price = price

		case "Autopilot Balanced Spot Pod Memory Requests (" + region + ")":
			pricing.spot_memory_balanced_price = price

		case "Autopilot Balanced Spot Pod mCPU Requests (" + region + ")":
			pricing.spot_cpu_balanced_price = price

		case "Autopilot Scale-Out x86 Spot Pod Memory Requests (" + region + ")":
			pricing.spot_memory_scaleout_price = price

		case "Autopilot Scale-Out x86 Spot Pod mCPU Requests (" + region + ")":
			pricing.spot_cpu_scaleout_price = price

		case "Autopilot Scale-Out Arm Spot Pod Memory Requests (" + region + ")":
			pricing.spot_arm_memory_scaleout_price = price

		case "Autopilot Scale-Out Arm Spot Pod mCPU Requests (" + region + ")":
			pricing.spot_arm_cpu_scaleout_price = price

		}

	}

	return pricing, nil
}
