package conf

import (
	"google.golang.org/protobuf/types/known/durationpb"
	"testing"
	"time"
)

func TestLiveModelCommandCanUseConfiguredFiniteBusinessBudget(t *testing.T) {
	config := validConfig()
	config.Command = commandConfiguration()
	config.Command.Model = modelConnectionConfiguration()
	config.Command.Model.Timeout = durationpb.New(time.Hour)
	config.Server.Grpc.Timeout = durationpb.New(time.Hour)
	if err := config.Validate(); err != nil {
		t.Fatal("one-hour fixed Model/Prepare budget rejected", err)
	}
	config.Server.Grpc.Timeout = durationpb.New(24*time.Hour + time.Second)
	if err := config.Validate(); err == nil {
		t.Fatal("unbounded Model command duration accepted")
	}
	config.Server.Grpc.Timeout = durationpb.New(time.Hour)
	config.Server.Admin.Timeout = durationpb.New(31 * time.Second)
	if err := config.Validate(); err == nil {
		t.Fatal("business budget changed the admin budget")
	}
}
