package lua

import (
	"encoding/json"
	"testing"
)

var script = `
	userfn = function ()
		c = "ok"
		local jj = 77
		local fnA = function ()
			jj = jj + 1
			kk.a = kk.a + 1
			kk.b = (kk.b + 1) * 2
			local aa = ""
			d = function()
				aa = aa .. "ha"
				return aa
			end
			return jj
		end
		local fnB = function ()
			jj = jj * 2
			return jj
		end
		jj = jj + 1
		return fnA, fnB
	end
	builtin1 = function ()
		return 1
	end
	builtin2 = function ()
		return 2
	end
	env = {kk = {a=1,b=2},}
	setfenv(userfn, env)
	`

func TestCheckpoint(t *testing.T) {
	initState := func() (*LState, *LFunction, *LFunction) {
		state := NewState()
		state.OpenLibs()
		err := state.DoString(script)
		if err != nil {
			t.Fatal(err)
		}
		value := state.GetGlobal("userfn")
		err = state.CallByParam(P{
			Fn:   value,
			NRet: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		fnA := state.ToFunction(-1)
		fnB := state.ToFunction(-2)
		state.Pop(2)
		return state, fnA, fnB
	}
	state, fnA, fnB := initState()
	defer state.Close()
	//do something
	for iter := 0; iter < 10; iter++ {
		err := state.CallByParam(P{Fn: fnA, NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		state.Pop(1)
		err = state.CallByParam(P{Fn: fnB, NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		state.Pop(1)
	}
	for iter := 0; iter < 10; iter++ {
		err := state.CallByParam(P{Fn: fnA.Env.RawGetString("d"), NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		state.Pop(1)
	}
	//Checkpoint
	bIn1 := state.GetGlobal("builtin1").(*LFunction)
	bIn2 := state.NewFunction(LGFunction(func(s *LState) int {
		s.Push(LNumber(3.0))
		return 1
	}))
	fnA.Env.RawSetString("LBuiltin", bIn1)
	fnA.Env.RawSetString("GBuiltin", bIn2)
	cp, err := Checkpoint(map[LValue]string{bIn1: "lua", bIn2: "go"}, state.GetGlobal("userfn").(*LFunction).Proto, fnA.Env, fnA, fnB)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Checkpoint:")
	bytes, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		panic(err)
	}
	t.Log(string(bytes))
	//Init new state
	//Load it
	stateDup, fnADup, fnBDup := initState()
	defer stateDup.Close()
	bIn1 = stateDup.GetGlobal("builtin2").(*LFunction)
	bIn2 = stateDup.NewFunction(LGFunction(func(s *LState) int {
		s.Push(LNumber(4.0))
		return 1
	}))
	fnADup.Env.RawSetString("LBuiltin", bIn1)
	fnADup.Env.RawSetString("GBuiltin", bIn2)
	err = LoadCheckpoint(cp, map[LValue]string{bIn1: "lua", bIn2: "go"}, stateDup.GetGlobal("userfn").(*LFunction).Proto, fnADup.Env, fnADup, fnBDup)
	if err != nil {
		t.Fatal(err)
	}
	for iter := 0; iter < 10; iter++ {
		err := state.CallByParam(P{Fn: fnA.Env.RawGetString("d"), NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		ret := state.ToString(-1)
		state.Pop(1)

		err = stateDup.CallByParam(P{Fn: fnADup.Env.RawGetString("d"), NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		retDup := stateDup.ToString(-1)
		stateDup.Pop(1)
		if ret != retDup {
			t.Error("Function d has different answer")
		}
		t.Log("D has result:", retDup)
	}
	for iter := 0; iter < 10; iter++ {
		err := state.CallByParam(P{Fn: fnA, NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		fnARet := state.ToNumber(-1)
		state.Pop(1)
		err = state.CallByParam(P{Fn: fnB, NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		fnBRet := state.ToNumber(-1)
		state.Pop(1)

		err = stateDup.CallByParam(P{Fn: fnADup, NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		fnARetDup := stateDup.ToNumber(-1)
		stateDup.Pop(1)
		err = stateDup.CallByParam(P{Fn: fnBDup, NRet: 1})
		if err != nil {
			t.Fatal(err)
		}
		fnBRetDup := stateDup.ToNumber(-1)
		stateDup.Pop(1)

		if fnARet != fnARetDup || fnBRet != fnBRetDup {
			t.Fatal("FnA or FnB has different answer")
		}
		t.Log("FnA result:", fnARet)
		t.Log("FnB result:", fnBRet)

		envA := fnA.Env.RawGetString("kk").(*LTable).RawGetString("a").(LNumber)
		envADup := fnADup.Env.RawGetString("kk").(*LTable).RawGetString("a").(LNumber)
		if envA != envADup {
			t.Fatal("env.a has different answer")
		}
		t.Log("env.a:", envA)
	}
	t.Log("Test builtin function")
	err = state.CallByParam(P{Fn: fnA.Env.RawGetString("LBuiltin"), NRet: 1})
	if err != nil {
		t.Fatal(err)
	}
	if state.ToNumber(-1) != LNumber(1) {
		t.Fatal("Wrong value for original LBuiltin")
	}
	state.Pop(1)
	err = stateDup.CallByParam(P{Fn: fnADup.Env.RawGetString("LBuiltin"), NRet: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stateDup.ToNumber(-1) != LNumber(2) {
		t.Fatal("Wrong value for Dup LBuiltin")
	}
	stateDup.Pop(1)
	err = state.CallByParam(P{Fn: fnA.Env.RawGetString("GBuiltin"), NRet: 1})
	if err != nil {
		t.Fatal(err)
	}
	if state.ToNumber(-1) != LNumber(3) {
		t.Fatal("Wrong value for original GBuiltin")
	}
	state.Pop(1)
	err = stateDup.CallByParam(P{Fn: fnADup.Env.RawGetString("GBuiltin"), NRet: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stateDup.ToNumber(-1) != LNumber(4) {
		t.Fatal("Wrong value for Dup GBuiltin")
	}
	stateDup.Pop(1)
}
