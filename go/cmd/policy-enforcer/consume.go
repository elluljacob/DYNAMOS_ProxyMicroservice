package main

import (
	"context"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// handleIncomingMessages routes incoming messages to the appropriate handler.
func (app *Application) handleIncomingMessages(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	switch grpcMsg.Type {
	case "requestApproval":
		return app.handleRequestApproval(ctx, grpcMsg)
	case "policyUpdate":
		return app.handlePolicyUpdate(ctx, grpcMsg)
	case "agreementUpdate":
		return app.handleAgreementUpdate(ctx, grpcMsg)
	default:
		app.logger.Error("Unknown message type", zap.String("type", grpcMsg.Type))
		return fmt.Errorf("unknown message type: %s", grpcMsg.Type)
	}
}

// handleAgreementUpdate validates and persists a new agreement or eFLINT model.
func (app *Application) handleAgreementUpdate(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: handleAgreementUpdate", grpcMsg.Traces)
	if err != nil {
		app.logger.Error("Error starting trace", zap.Error(err))
	}
	defer span.End()

	var agreementUpdate pb.PolicyUpdate
	if err := grpcMsg.Body.UnmarshalTo(&agreementUpdate); err != nil {
		app.logger.Error("Failed to unmarshal agreement update", zap.Error(err))
		return fmt.Errorf("failed to unmarshal agreement update: %w", err)
	}

	app.logger.Info("Processing agreement update",
		zap.String("agreementName", agreementUpdate.AgreementName),
	)

	// Validate & Persist
	err = app.validationService.ValidateAndPersistAgreement(ctx, agreementUpdate.AgreementName, agreementUpdate.AgreementPayload)

	// Set response properties
	if agreementUpdate.ValidationResponse == nil {
		agreementUpdate.ValidationResponse = &pb.ValidationResponse{}
	}

	if err != nil {
		app.logger.Error("Agreement validation failed", zap.Error(err))
		agreementUpdate.ValidationResponse.RequestApproved = false
	} else {
		agreementUpdate.ValidationResponse.RequestApproved = true
	}

	agreementUpdate.RequestMetadata.DestinationQueue = "orchestrator-in"

	if err := app.responseSender.SendPolicyUpdate(ctx, &agreementUpdate); err != nil {
		app.logger.Error("Failed to send agreement update response", zap.Error(err))
		return fmt.Errorf("failed to send agreement update response: %w", err)
	}

	return nil
}

// handleRequestApproval processes a request approval message.
func (app *Application) handleRequestApproval(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: handleRequestApproval", grpcMsg.Traces)
	if err != nil {
		app.logger.Error("Error starting trace", zap.Error(err))
	}
	defer span.End()

	var requestApproval pb.RequestApproval
	if err := grpcMsg.Body.UnmarshalTo(&requestApproval); err != nil {
		app.logger.Error("Failed to unmarshal request approval", zap.Error(err))
		return fmt.Errorf("failed to unmarshal request approval: %w", err)
	}

	app.logger.Info("Processing request approval",
		zap.String("userName", requestApproval.User.UserName),
		zap.String("userId", requestApproval.User.Id),
	)

	// Validate the request using the validation service
	response := app.validationService.ValidateRequest(ctx, &requestApproval)

	// Send the validation response
	if err := app.responseSender.SendValidationResponse(ctx, response); err != nil {
		app.logger.Error("Failed to send validation response", zap.Error(err))
		return fmt.Errorf("failed to send validation response: %w", err)
	}

	return nil
}

// handlePolicyUpdate processes a policy update message.
func (app *Application) handlePolicyUpdate(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: handlePolicyUpdate", grpcMsg.Traces)
	if err != nil {
		app.logger.Error("Error starting trace", zap.Error(err))
	}
	defer span.End()

	var policyUpdate pb.PolicyUpdate
	if err := grpcMsg.Body.UnmarshalTo(&policyUpdate); err != nil {
		app.logger.Error("Failed to unmarshal policy update", zap.Error(err))
		return fmt.Errorf("failed to unmarshal policy update: %w", err)
	}

	app.logger.Info("Processing policy update",
		zap.String("userName", policyUpdate.User.UserName),
	)

	// Create a request approval from the policy update to reuse validation logic
	requestApproval := &pb.RequestApproval{
		Type:          policyUpdate.Type,
		User:          policyUpdate.User,
		DataProviders: policyUpdate.DataProviders,
	}

	// Validate using the same validation service
	validationResponse := app.validationService.ValidateRequest(ctx, requestApproval)

	// Override the type for policy updates
	validationResponse.Type = "policyUpdate"

	// Set up the policy update response
	policyUpdate.ValidationResponse = validationResponse
	policyUpdate.RequestMetadata.DestinationQueue = "orchestrator-in"

	// Send the policy update response
	if err := app.responseSender.SendPolicyUpdate(ctx, &policyUpdate); err != nil {
		app.logger.Error("Failed to send policy update", zap.Error(err))
		return fmt.Errorf("failed to send policy update: %w", err)
	}

	return nil
}
