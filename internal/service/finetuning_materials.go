package service

import (
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"strconv"
)

func parameterView(p cpup01.Parameter) *trainingv1.Parameter {
	value := &trainingv1.Parameter{Name: p.Name}
	switch p.Type {
	case "INTEGER":
		v, e := strconv.ParseInt(p.Value, 10, 64)
		if e != nil || strconv.FormatInt(v, 10) != p.Value {
			return nil
		}
		value.Value = &trainingv1.Parameter_IntegerValue{IntegerValue: v}
	case "DECIMAL":
		value.Value = &trainingv1.Parameter_DecimalValue{DecimalValue: p.Value}
	default:
		return nil
	}
	return value
}
