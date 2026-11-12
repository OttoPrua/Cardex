package sched

import (
	"errors"
	"testing"

	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestDeliveryErrorPartsUnknownAdapterIsNotFailure(t *testing.T) {
	cause := errors.Join(&notify.UnknownAdapterError{Key: "origin", Kind: "cutover"})
	if skip, failed := deliveryErrorParts(cause); skip || failed {
		t.Fatalf("skip %v failed %v", skip, failed)
	}
	cause = errors.Join(&notify.UnknownAdapterError{Key: "origin", Kind: "cutover"}, errors.New("exec: lark failed"))
	if _, failed := deliveryErrorParts(cause); !failed {
		t.Fatal("real hop failure was hidden")
	}
}
