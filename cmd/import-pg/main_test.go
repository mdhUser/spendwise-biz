package main

import "testing"

func TestDeterministicIDIsStableAndTableScoped(t *testing.T) {
	first := deterministicID("products", "prod-1")
	second := deterministicID("products", "prod-1")
	otherTable := deterministicID("offers", "prod-1")
	if first != second {
		t.Fatal("deterministicID must be stable")
	}
	if first == otherTable {
		t.Fatal("deterministicID must include the source table")
	}
	if targetTable("products") != "catalog_products" || targetTable("offers") != "catalog_offers" {
		t.Fatal("unexpected target table mapping")
	}
}
