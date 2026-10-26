/*
Copyright 2025 The local-ccm Authors.

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

package main

import (
	"slices"
	"testing"

	v1 "k8s.io/api/core/v1"
)

// --- addressesEqual -------------------------------------------------------

func TestAddressesEqual_IdenticalSlices(t *testing.T) {
	a := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	b := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	if !addressesEqual(a, b) {
		t.Fatalf("identical slices should be equal")
	}
}

func TestAddressesEqual_DifferentOrderIsEqual(t *testing.T) {
	a := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	b := []v1.NodeAddress{
		{Type: v1.NodeHostName, Address: "node-1"},
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
	}
	if !addressesEqual(a, b) {
		t.Fatalf("slices differing only in order should be equal")
	}
}

func TestAddressesEqual_DifferentLength(t *testing.T) {
	a := []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: "10.0.0.1"}}
	b := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	if addressesEqual(a, b) {
		t.Fatalf("slices of different length should not be equal")
	}
}

func TestAddressesEqual_SameLengthDifferentContent(t *testing.T) {
	a := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	b := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.2"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	if addressesEqual(a, b) {
		t.Fatalf("slices with different content should not be equal")
	}
}

// TestAddressesEqual_DualStackDuplicateTypeBothSides is the regression test
// for the production bug. Both sides carry two InternalIP entries (dual-stack).
// The current (buggy) implementation keys a map by string(addr.Type) and only
// iterates aMap, so duplicate InternalIP entries collapse and the comparison
// returns true incorrectly. After the fix this must still pass (it is a valid
// equality).
func TestAddressesEqual_DualStackDuplicateTypeBothSides(t *testing.T) {
	a := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	b := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeHostName, Address: "node-1"},
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
	}
	if !addressesEqual(a, b) {
		t.Fatalf("dual-stack slices with same duplicate-type addresses in different order should be equal")
	}
}

// TestAddressesEqual_BugCase_2InternalVersus1Internal1External is the exact
// production failure: a has two InternalIPs, b has one InternalIP + one
// ExternalIP. Same length (3), same single InternalIP value after the
// map collapse. The current (buggy) implementation returns true; it must
// return false.
func TestAddressesEqual_BugCase_2InternalVersus1Internal1External(t *testing.T) {
	a := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	b := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeExternalIP, Address: "203.0.113.5"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	if addressesEqual(a, b) {
		t.Fatalf("2x InternalIP must not be equal to 1x InternalIP + 1x ExternalIP")
	}
}

// --- computeAddresses -----------------------------------------------------

// containsAddress is a small test helper that does an order-independent
// membership check without relying on the production addressesEqual.
func containsAddress(addrs []v1.NodeAddress, want v1.NodeAddress) bool {
	return slices.ContainsFunc(addrs, func(got v1.NodeAddress) bool {
		return got.Type == want.Type && got.Address == want.Address
	})
}

func TestComputeAddresses_DualStackNewExternalIP_PreservesBothInternalIPs(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	got := computeAddresses(current, "", "", "203.0.113.5")

	if len(got) != 4 {
		t.Fatalf("expected 4 addresses (2 InternalIP + ExternalIP + Hostname), got %d: %v", len(got), got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "100.64.0.1"}) {
		t.Errorf("IPv4 InternalIP must be preserved, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "fd7a::1"}) {
		t.Errorf("IPv6 InternalIP must be preserved, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeExternalIP, Address: "203.0.113.5"}) {
		t.Errorf("ExternalIP must be added, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeHostName, Address: "node-1"}) {
		t.Errorf("Hostname must be preserved, got: %v", got)
	}
}

func TestComputeAddresses_SingleStack_AddsExternalIP(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	got := computeAddresses(current, "", "", "203.0.113.5")

	if len(got) != 3 {
		t.Fatalf("expected 3 addresses, got %d: %v", len(got), got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "10.0.0.1"}) {
		t.Errorf("InternalIP must be preserved, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeExternalIP, Address: "203.0.113.5"}) {
		t.Errorf("ExternalIP must be added, got: %v", got)
	}
}

func TestComputeAddresses_ExternalIPEqualsInternalIP_NoExternalIP(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "203.0.113.5"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	got := computeAddresses(current, "", "", "203.0.113.5")

	for _, a := range got {
		if a.Type == v1.NodeExternalIP {
			t.Fatalf("ExternalIP must not be present when it equals InternalIP, got: %v", got)
		}
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "203.0.113.5"}) {
		t.Errorf("InternalIP must be preserved, got: %v", got)
	}
}

// TestComputeAddresses_ExternalIPEqualsOneOfTwoInternalIPs_NoExternalIP covers
// the dual-stack edge case where the detected external IP happens to equal one
// of the existing InternalIP values. We must not add an ExternalIP that
// duplicates an InternalIP, and both InternalIPs must survive.
func TestComputeAddresses_ExternalIPEqualsOneOfTwoInternalIPs_NoExternalIP(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
		{Type: v1.NodeInternalIP, Address: "203.0.113.5"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	got := computeAddresses(current, "", "", "203.0.113.5")

	for _, a := range got {
		if a.Type == v1.NodeExternalIP {
			t.Fatalf("ExternalIP must not be present when it equals an InternalIP, got: %v", got)
		}
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "100.64.0.1"}) {
		t.Errorf("first InternalIP must be preserved, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "203.0.113.5"}) {
		t.Errorf("second InternalIP must be preserved, got: %v", got)
	}
}

func TestComputeAddresses_PreserveKubeletInternalIPs_AllPreserved(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	got := computeAddresses(current, "", "", "203.0.113.5")

	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "100.64.0.1"}) {
		t.Errorf("IPv4 InternalIP must be preserved when internalIPTarget is empty, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "fd7a::1"}) {
		t.Errorf("IPv6 InternalIP must be preserved when internalIPTarget is empty, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeExternalIP, Address: "203.0.113.5"}) {
		t.Errorf("ExternalIP must be added, got: %v", got)
	}
}

func TestComputeAddresses_InternalIPTargetSet_ReplacesInternalIPs(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "100.64.0.1"},
		{Type: v1.NodeInternalIP, Address: "fd7a::1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	// internalIPTarget set: local-ccm owns InternalIP and replaces all
	// existing ones with the single detected value.
	got := computeAddresses(current, "10.0.0.1", "10.0.0.1", "203.0.113.5")

	internalCount := 0
	for _, a := range got {
		if a.Type == v1.NodeInternalIP {
			internalCount++
			if a.Address != "10.0.0.1" {
				t.Errorf("InternalIP must be the detected value, got %s, got: %v", a.Address, got)
			}
		}
	}
	if internalCount != 1 {
		t.Fatalf("expected exactly 1 InternalIP after internalIPTarget-driven replacement, got %d: %v", internalCount, got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeExternalIP, Address: "203.0.113.5"}) {
		t.Errorf("ExternalIP must still be added, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeHostName, Address: "node-1"}) {
		t.Errorf("Hostname must be preserved, got: %v", got)
	}
}

func TestComputeAddresses_ExistingExternalIPReplaced(t *testing.T) {
	current := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
		{Type: v1.NodeExternalIP, Address: "198.51.100.1"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	got := computeAddresses(current, "", "", "203.0.113.5")

	for _, a := range got {
		if a.Type == v1.NodeExternalIP && a.Address != "203.0.113.5" {
			t.Errorf("stale ExternalIP %s must be replaced with 203.0.113.5, got: %v", a.Address, got)
		}
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeExternalIP, Address: "203.0.113.5"}) {
		t.Errorf("new ExternalIP must be present, got: %v", got)
	}
	if !containsAddress(got, v1.NodeAddress{Type: v1.NodeInternalIP, Address: "10.0.0.1"}) {
		t.Errorf("InternalIP must be preserved, got: %v", got)
	}
}
