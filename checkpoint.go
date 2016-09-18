package lua

import (
	"errors"
	"fmt"
	"reflect"
)

var ErrInvalidForFlatten = errors.New("存在不支持Flatten的对象")
var ErrCannotFlattenLGFunction = errors.New("不支持FlattenGo函数")
var ErrUpvalueNotClosed = errors.New("Flatten时发现Upvalue没有处于关闭状态")

func isStatic(value LValue) bool {
	return value.Type() == LTNumber || value.Type() == LTString || value.Type() == LTNil
}

func uint64Pointer(target interface{}) uint64 {
	return uint64(reflect.ValueOf(target).Pointer())
}

func toPLValue(value LValue) *PLValue {
	switch value.Type() {
	case LTNil:
		return &PLValue{Value: &PLValue_Nil{Nil: true}}
	case LTNumber:
		return &PLValue{Value: &PLValue_Number{Number: float64(value.(LNumber))}}
	case LTString:
		return &PLValue{Value: &PLValue_Str{Str: string(value.(LString))}}
	default:
	}
	return &PLValue{Value: &PLValue_Ptr{Ptr: uint64Pointer(value)}}
}

func ToPElement(value LValue, builtin map[LValue]string) *PElement {
	if name, exist := builtin[value]; exist {
		return &PElement{Element: &PElement_Builtin{Builtin: name}}
	}
	switch value.Type() {
	case LTTable:
		table := value.(*LTable)
		pTable := &PLTable{}
		pTable.Array = make([]*PLValue, len(table.array))
		for i, v := range table.array {
			pTable.Array[i] = toPLValue(v)
		}
		length := len(table.strdict) + len(table.dict)
		pTable.Keys = make([]*PLValue, length)
		pTable.Values = make([]*PLValue, length)
		i := 0
		for k, v := range table.strdict {
			pTable.Keys[i] = &PLValue{Value: &PLValue_Str{Str: k}}
			pTable.Values[i] = toPLValue(v)
			i++
		}
		for k, v := range table.dict {
			pTable.Keys[i] = toPLValue(k)
			pTable.Values[i] = toPLValue(v)
			i++
		}
		return &PElement{Element: &PElement_Table{Table: pTable}}
	case LTFunction:
		fn := value.(*LFunction)
		if fn.IsG {
			panic(ErrCannotFlattenLGFunction.Error())
		}
		upvalues := make([]uint64, len(fn.Upvalues))
		for i := range fn.Upvalues {
			upvalues[i] = uint64Pointer(fn.Upvalues[i])
		}
		return &PElement{Element: &PElement_Fn{
			Fn: &PLFunction{
				Env:      uint64Pointer(fn.Env),
				Proto:    uint64Pointer(fn.Proto),
				Upvalues: upvalues,
			}}}
	}
	return nil
}

func Checkpoint(builtin map[LValue]string, rootProto *FunctionProto, targets ...LValue) (*PCheckpoint, error) {
	gotten := make(map[LValue]bool)
	upvalues := make(map[*Upvalue]bool)
	for k := range builtin {
		if !isStatic(k) {
			gotten[k] = true
		}
	}
	for _, target := range targets {
		err := FlattenVars(gotten, upvalues, target)
		if err != nil {
			return nil, err
		}
	}
	protos := GetFuncProtoIdx(rootProto)
	ret := &PCheckpoint{
		Gotten:   make(map[uint64]*PElement),
		Protos:   make(map[uint64]*PFnProto),
		Upvalues: make(map[uint64]*PUpvalue),
		Targets:  make([]uint64, 0, len(targets)),
	}
	for i := range protos {
		ret.Protos[uint64Pointer(protos[i])] = &PFnProto{Idx: uint64(i)}
	}
	for v := range upvalues {
		ptr := uint64Pointer(v)
		ret.Upvalues[ptr] = &PUpvalue{Value: toPLValue(v.value)}
	}
	for _, v := range targets {
		ptr := uint64Pointer(v)
		ret.Targets = append(ret.Targets, ptr)
	}
	for v := range gotten {
		ptr := uint64Pointer(v)
		ret.Gotten[ptr] = ToPElement(v, builtin)
	}
	return ret, nil
}

func LoadCheckpoint(checkpoint *PCheckpoint, builtin map[LValue]string, rootProto *FunctionProto, targets ...LValue) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case string:
				err = errors.New(v)
			case error:
				err = v
			default:
				err = fmt.Errorf("Recover from panic: %s", reflect.ValueOf(r).String())
			}
		}
	}()
	inverseBuiltin := make(map[string]LValue)
	for value, key := range builtin {
		inverseBuiltin[key] = value
	}
	if len(targets) != len(checkpoint.Targets) {
		err = errors.New("targets num is not same")
		return err
	}
	for i := range targets {
		if targets[i].Type() != LTTable && targets[i].Type() != LTFunction {
			err = errors.New("Do not support load checkpoint on such type of target")
			return
		}
	}
	protos := GetFuncProtoIdx(rootProto)
	if len(protos) != len(checkpoint.Protos) {
		return errors.New("protos num is not same")
	}
	gotten := make(map[uint64]LValue)
	upvalues := make(map[uint64]*Upvalue)
	var getOrBuild func(ptr uint64) LValue
	getOrBuildByPLValue := func(value *PLValue) LValue {
		switch v := value.Value.(type) {
		case *PLValue_Number:
			return LNumber(v.Number)
		case *PLValue_Str:
			return LString(v.Str)
		case *PLValue_Nil:
			return LNil
		default:
		}
		return getOrBuild(value.GetPtr())
	}
	getOrBuildUpValue := func(ptr uint64) *Upvalue {
		if uv, exist := upvalues[ptr]; exist {
			return uv
		}
		upvalue := &Upvalue{closed: true}
		upvalues[ptr] = upvalue
		upvalue.value = getOrBuildByPLValue(checkpoint.Upvalues[ptr].Value)
		return upvalue
	}
	getOrBuild = func(ptr uint64) LValue {
		if value, ok := gotten[ptr]; ok {
			return value
		}
		element := checkpoint.Gotten[ptr]
		if fn := element.GetFn(); fn != nil {
			idx, exist := checkpoint.Protos[fn.Proto]
			if !exist {
				panic("No such proto in checkpoint")
			}
			proto := protos[int(idx.Idx)]
			if int(proto.NumUpvalues) != len(fn.Upvalues) {
				panic("Upvalues for function is not right")
			}
			lFn := newLFunctionL(proto, nil, int(proto.NumUpvalues))
			gotten[ptr] = lFn
			lFn.Env = getOrBuild(fn.Env).(*LTable)
			for i := range fn.Upvalues {
				lFn.Upvalues[i] = getOrBuildUpValue(fn.Upvalues[i])
			}

		} else if table := element.GetTable(); table != nil {
			lTable := newLTable(len(table.Array), len(table.Keys))
			gotten[ptr] = lTable
			for i := range table.Array {
				lTable.Append(getOrBuildByPLValue(table.Array[i]))
			}
			for i, k := range table.Keys {
				v := getOrBuildByPLValue(table.Values[i])

				if str, ok := k.Value.(*PLValue_Str); ok {
					lTable.strdict[str.Str] = v
				} else {
					kk := getOrBuildByPLValue(k)
					lTable.dict[kk] = v
				}
			}
		} else {
			var exist bool
			gotten[ptr], exist = inverseBuiltin[element.GetBuiltin()]
			if !exist {
				panic("no such builtin module")
			}
		}
		return gotten[ptr]
	}
	for i := range targets {
		ptr := checkpoint.Targets[i]
		targetInCP := getOrBuild(ptr)
		switch value := targets[i].(type) {
		case *LTable:
			if table, ok := targetInCP.(*LTable); ok {
				*value = *table
			} else {
				err = errors.New("target is not same type of the target in checkpoint")
				return
			}
		case *LFunction:
			if fn, ok := targetInCP.(*LFunction); ok {
				*value = *fn
			} else {
				err = errors.New("target is not same type of the target in checkpoint")
				return
			}
		}
	}
	err = nil
	return
}

func FlattenVars(gottenVars map[LValue]bool, gottenUpvalues map[*Upvalue]bool, target LValue) error {
	if gottenVars[target] {
		return nil
	}
	switch target.Type() {
	case LTNil:
	case LTNumber:
	case LTString:
	case LTBool:
	case LTFunction:
		lFunction := target.(*LFunction)
		if lFunction.IsG {
			return ErrCannotFlattenLGFunction
		}
		gottenVars[target] = true
		for _, upvalue := range lFunction.Upvalues {
			if gottenUpvalues[upvalue] {
				continue
			}
			if !upvalue.IsClosed() {
				return ErrUpvalueNotClosed
			}
			gottenUpvalues[upvalue] = true
			err := FlattenVars(gottenVars, gottenUpvalues, upvalue.value)
			if err != nil {
				return err
			}
		}
		err := FlattenVars(gottenVars, gottenUpvalues, lFunction.Env)
		if err != nil {
			return err
		}
	case LTTable:
		lTable := target.(*LTable)
		gottenVars[target] = true
		for _, v := range lTable.array {
			err := FlattenVars(gottenVars, gottenUpvalues, v)
			if err != nil {
				return err
			}
		}
		for _, v := range lTable.strdict {
			err := FlattenVars(gottenVars, gottenUpvalues, v)
			if err != nil {
				return err
			}
		}
		for k, v := range lTable.dict {
			err := FlattenVars(gottenVars, gottenUpvalues, k)
			if err != nil {
				return err
			}
			err = FlattenVars(gottenVars, gottenUpvalues, v)
			if err != nil {
				return err
			}
		}
	case LTChannel:
		fallthrough
	case LTThread:
		fallthrough
	case LTUserData:
	default:
		return ErrInvalidForFlatten
	}
	return nil
}

//GetFuncProtoIdx get Idx by recursive visiting
func GetFuncProtoIdx(proto *FunctionProto) []*FunctionProto {
	protos := []*FunctionProto{proto}
	for _, p := range proto.FunctionPrototypes {
		protos = append(protos, GetFuncProtoIdx(p)...)
	}
	return protos
}
