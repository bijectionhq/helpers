package surface

import (
	"concave/internal/repository"
	"errors"
	"testing"
)

func TestCompilerProtocolRejectsAmbiguousOrIncompleteResponses(t *testing.T) {
	for _, data := range []string{
		`{"protocol":1,"analysis":{"entries":{},"inputs":{}},"timings":{"compile":-1}}`,
		`{}`, `{"protocol":2,"analysis":{"entries":{},"inputs":{}}}`,
		`{"protocol":1,"analysis":{}}`, `{"protocol":1,"analysis":{"entries":{},"inputs":{},"unknown":true}}`,
		`{"protocol":1,"analysis":{"entries":{},"inputs":{}}} {}`,
		`{"protocol":1,"analysis":{"entries":{},"inputs":{}},"error":{"kind":"stale_inputs","message":"changed"}}`,
	} {
		if _, err := decodeResponse([]byte(data)); err == nil {
			t.Fatal("invalid protocol accepted", data)
		}
	}
	if _, err := decodeResponse([]byte(`{"protocol":1,"analysis":{"entries":{},"inputs":{}}}`)); err != nil {
		t.Fatal(err)
	}
	var stale *repository.Stale
	if _, err := decodeResponse([]byte(`{"protocol":1,"error":{"kind":"stale_inputs","message":"changed"}}`)); !errors.As(err, &stale) {
		t.Fatal("staleness lost at compiler boundary", err)
	}
}
