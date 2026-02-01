package main

import (
	"context"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	"github.com/Jorrit05/DYNAMOS/pkg/msinit"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

// SidecarHandler is the background listener for rabbitMQ triggers (from my understanding)
func SidecarHandler(config *msinit.Configuration) func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
	return func(ctx context.Context, msComm *pb.MicroserviceCommunication) error {
		// Start Tracing Span
		ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: messageHandler", msComm.Traces)
		if err != nil {
			logger.Sugar().Warnf("Error starting span: %v", err)
		}
		defer span.End()

		// Route based on request type
		switch msComm.RequestType {
		case "sqlDataRequest":
			logger.Debug("[EXT_PROVIDER] Received request via RabbitMQ.")
		default:
			logger.Sugar().Debugf("Received request type: %v", msComm.RequestType)
		}

		return nil
	}
}
