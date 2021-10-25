package gjrpc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryPanic(t *testing.T) {
	r := &registry{}
	_, err := r.registerCallback("cb", func() { panic(errors.New("panic error")) })
	if err != nil {
		t.Fatalf("Unable to register callback: %v", err)
	}
	r.muteCrashes = true
	_, err = r.call(context.Background(), nil, "cb")
	r.muteCrashes = false
	if err == nil || err.Error() != "method handler crashed" {
		t.Errorf("got wrong error: %v", err)
	}
}

func TestRegistryContext(t *testing.T) {
	r := &registry{}
	_, err := r.registerCallback("cb", contextFunction)
	if err != nil {
		t.Fatalf("Unable to register callback: %v", err)
	}
	childCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = r.call(childCtx, nil, "cb", 200*time.Millisecond)
	if err != context.DeadlineExceeded {
		t.Errorf("context error should be DeadlineExceeded, getting %v", err)
	}
}

func TestRegistryErrorResult(t *testing.T) {
	r := &registry{}
	_, err := r.registerCallback("cb", errorFunction)
	if err != nil {
		t.Fatalf("Unable to register callback: %v", err)
	}
	res, err := r.call(context.Background(), nil, "cb")
	innerRes, innerErr := res.([]interface{})[0], res.([]interface{})[1].(error)
	if innerRes != nil {
		t.Errorf("result should me nil, getting %v", res)
	}
	if err == nil || err.(error).Error() != "Some test error" {
		t.Errorf("wrong error: %v", err)
	}
	if innerErr != err {
		t.Errorf("error inside the result should be equal to comuted error")
	}
}

func TestRegistryFunction(t *testing.T) {
	r := &registry{}
	_, err := r.registerCallback("cb", callbackFunction)
	if err != nil {
		t.Fatalf("Unable to register callback: %v", err)
	}
	res, err := r.call(context.TODO(), nil, "cb", 1, 2)
	if err != nil {
		t.Errorf("err isn't nil: %v", err)
	}
	if res != 3 {
		t.Errorf("result != 3")
	}
}

func TestRegistryMethod(t *testing.T) {
	r := &registry{}
	a := &A{}
	_, err := r.registerCallback("cb", a.methodFunction)
	if err != nil {
		t.Fatalf("Unable to register callback: %v", err)
	}
	res, err := r.call(context.TODO(), nil, "cb", 2, 3)
	if err != nil {
		t.Errorf("err isn't nil: %v", err)
	}
	if res != 6 {
		t.Errorf("result != 6")
	}
}

func TestRegistryLambda(t *testing.T) {
	r := &registry{}
	_, err := r.registerCallback("cb", func(a, b int) int { return a + b + 1000 })
	if err != nil {
		t.Fatalf("Unable to register callback: %v", err)
	}
	res, err := r.call(context.TODO(), nil, "cb", 2, 3)
	if err != nil {
		t.Errorf("err isn't nil: %v", err)
	}
	if res != 1005 {
		t.Errorf("result != 1005")
	}
}

// func TestRegistryMultiOut(t *testing.T) {
// 	r := &registry{}
// 	_, err := r.registerCallback("cb", func() int { return 5 })
// 	if err != nil {
// 		t.Fatalf("Unable to register callback: %v", err)
// 	}
// 	f := func() []interface{} {
// 		return []interface{}{1, 2, 3}
// 	}
// 	res, err := r.call(context.TODO(), "cb")
// 	if err != nil {
// 		t.Errorf("err isn't nil: %v", err)
// 	}
// 	//
// }

func callbackFunction(a, b int) int {
	return a + b
}

func contextFunction(ctx context.Context, timeout time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return nil
	}
}

func errorFunction() (interface{}, error) {
	return nil, errors.New("Some test error")
}

type A struct{}

func (this *A) methodFunction(a, b int) int {
	return a * b
}
