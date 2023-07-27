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
	"math"
	"testing"
)

const float64EqualityThreshold = 1e-9

func TestValidateLowerLimits(t *testing.T) {
	// Test Case #1
	var cpu_want int64 = 1000
	var memory_want int64 = 1000
	var storage_want int64 = 1000

	cpu, memory, storage := ValidateLowerLimits(1000, 1000, 1000)
	if cpu != cpu_want || memory != memory_want || storage != storage_want {
		t.Fatalf(`ValidateLowerLimits(1000,1000,1000,COMPUTE_CLASS_REGULAR) = %d, %d, %d doesn't match expected %d %d %d`, cpu, memory, storage, cpu_want, memory_want, storage_want)
	}

	// Test Case #2
	cpu_want = 250
	memory_want = 500
	storage_want = 10

	cpu, memory, storage = ValidateLowerLimits(249, 499, 9)
	if cpu != cpu_want || memory != memory_want || storage != storage_want {
		t.Fatalf(`ValidateLowerLimits(10,10,5,COMPUTE_CLASS_REGULAR) = %d, %d, %d doesn't match expected %d %d %d`, cpu, memory, storage, cpu_want, memory_want, storage_want)
	}
}

func TestDecideComputeClass(t *testing.T) {
	// Test Case #1
	compute_class_want := COMPUTE_CLASS_REGULAR
	compute_class := DecideComputeClass("test-pod", 10000, 10000, false)

	if compute_class != compute_class_want {
		t.Fatalf(`DecideComputeClass(1000,1000,false) = %s doesn't match expected %s`, COMPUTE_CLASSES[compute_class], COMPUTE_CLASSES[compute_class_want])
	}

	// Test Case #2
	compute_class_want = COMPUTE_CLASS_BALANCED
	compute_class = DecideComputeClass("test-pod", 35000, 100000, false)

	if compute_class != compute_class_want {
		t.Fatalf(`DecideComputeClass(35000,100000,false) = %s doesn't match expected %s`, COMPUTE_CLASSES[compute_class], COMPUTE_CLASSES[compute_class_want])
	}

	// Test Case #3
	compute_class_want = COMPUTE_CLASS_SCALEOUT_ARM
	compute_class = DecideComputeClass("test-pod", 20000, 80000, true)

	if compute_class != compute_class_want {
		t.Fatalf(`DecideComputeClass(25000, 50000, true) = %s doesn't match expected %s`, COMPUTE_CLASSES[compute_class], COMPUTE_CLASSES[compute_class_want])
	}

}

func TestCalculatePricing(t *testing.T) {
	pricing := ap_pricing{
		region:        "test-region-1",
		storage_price: 0.0000706,

		// regular pricing
		cpu_price:             0.0573,
		memory_price:          0.0063421,
		cpu_balanced_price:    0.0831,
		memory_balanced_price: 0.0091933,
		cpu_scaleout_price:    0.0722,
		memory_scaleout_price: 0.0079911,

		// spot pricing
		spot_cpu_price:             0.0172,
		spot_memory_price:          0.0019026,
		spot_cpu_balanced_price:    0.0249,
		spot_memory_balanced_price: 0.002758,
		spot_cpu_scaleout_price:    0.0217,
		spot_memory_scaleout_price: 0.0023973,
	}

	// Test Case #1

	compute_class := DecideComputeClass("test-pod", 4000, 16000, false)
	price_want := 0.3313796 // 0.000706 (cpu price * 4) + 0.1014736 (memory price * 16) +0.2292 (storage price * 10)
	price := CalculatePricing(4000, 16000, 10000, pricing, compute_class, false)

	if !almostEqual(price, price_want) {
		t.Fatalf(`CalculatePricing(4000, 16000, 10000, {test-region-pricing}, %s, false) = %.7f doesn't match expected %.7f`, COMPUTE_CLASSES[compute_class], price, price_want)
	}

	// Test Case #2
	compute_class = DecideComputeClass("test-pod", 40000, 80000, false)
	price_want = 4.0601700 // 3.324 (cpu price * 40) + 0.735464 (memory price * 80) + 0.2292 (storage price * 10)
	price = CalculatePricing(40000, 80000, 10000, pricing, compute_class, false)

	if !almostEqual(price, price_want) {
		t.Fatalf(`CalculatePricing(4000, 16000, 10000, {test-region-pricing}, %s, false) = %.7f doesn't match expected %.7f`, COMPUTE_CLASSES[compute_class], price, price_want)
	}

	// Test Case #3
	compute_class = DecideComputeClass("test-pod", 25000, 100000, false)
	price_want = 0.6209660 // 0.43 (cpu spot price * 25) + 0.19026 (spot memory price * 100) + 0.000706 (spot storage price * 10)
	price = CalculatePricing(25000, 100000, 10000, pricing, compute_class, true)

	if !almostEqual(price, price_want) {
		t.Fatalf(`CalculatePricing(4000, 16000, 10000, {test-region-pricing}, %s, false) = %.7f doesn't match expected %.7f`, COMPUTE_CLASSES[compute_class], price, price_want)
	}

}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) <= float64EqualityThreshold
}
