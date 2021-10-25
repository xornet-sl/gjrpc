package gjrpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func executeM1(s string, i int, f float32, b bool, n interface{}) string {
	return "executeM1 OK"
}

func executeM2() error {
	return errors.New("some error")
}

func TestExecute(t *testing.T) {
	inStr := `[{"jsonrpc":"2.0","id":1,"method":"someMethod","params":["arg1",5,4.1,true,null]},
	{"jsonrpc":"2.0","id":2,"method":"someMethod2"}]`
	messages, _, err := parseMessage([]byte(inStr))
	if err != nil {
		t.Error(err)
	}
	var id uint64
	err = json.Unmarshal(messages[0].ID, &id)
	if err != nil {
		t.Error(err)
	}
	if id != 1 {
		t.Errorf("msg.ID = %d, shoud be %d", id, 1)
	}
	rpc := NewRpc(context.Background())
	err = rpc.RegisterMethod("someMethod", executeM1)
	if err != nil {
		t.Error(err)
	}
	err = rpc.RegisterMethod("someMethod2", executeM2)
	if err != nil {
		t.Error(err)
	}
	result := rpc.callBatch(nil, messages)
	j, err := json.Marshal(result)
	if err != nil {
		t.Error(err)
	}
	mustBe1 := `[{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"some error"}},{"jsonrpc":"2.0","id":1,"result":"executeM1 OK"}]`
	mustBe2 := `[{"jsonrpc":"2.0","id":1,"result":"executeM1 OK"},{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"some error"}}]`
	if string(j) != mustBe1 && string(j) != mustBe2 {
		t.Errorf("'%s' != '%s'", string(j), mustBe1)
	}
}

func TestCompile(t *testing.T) {
	msg, err := requestMessage(1, "someMethod", "arg1", 5, 4.1, true, nil)
	if err != nil {
		t.Error(err)
	}
	j, err := json.Marshal(msg)
	if err != nil {
		t.Error(err)
	}
	mustBe := `{"jsonrpc":"2.0","id":1,"method":"someMethod","params":["arg1",5,4.1,true,null]}`
	if string(j) != mustBe {
		t.Errorf("'%s' != '%s'", j, mustBe)
	}
}
