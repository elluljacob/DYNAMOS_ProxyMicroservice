package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func archetypesHandler(etcdClient *clientv3.Client, root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Info("Entering archetypesHandler")
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.Archetype](w, r, etcdClient, "/archetypes")
		case http.MethodPut:
			// Call your handler for PUT
			archetype := &api.Archetype{}
			api.GenericPutToEtcd[api.Archetype](w, r, etcdClient, "/archetypes", archetype)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func requestTypesHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.RequestType](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			// Call your handler for PUT
			requestType := &api.RequestType{}
			api.GenericPutToEtcd[api.RequestType](w, r, etcdClient, etcdRoot, requestType)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func microserviceMetadataHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.MicroserviceMetadata](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			// Call your handler for PUT
			msMetadata := &api.MicroserviceMetadata{}
			api.GenericPutToEtcd[api.MicroserviceMetadata](w, r, etcdClient, etcdRoot, msMetadata)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func agreementsHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.Agreement](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			handlePutAgreement(w, r)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handlePutAgreement(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		logger.Sugar().Errorf("Error reading body: %v", err)
		http.Error(w, "Error reading request body", http.StatusBadRequest)
		return
	}

	var nameExtractor struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &nameExtractor); err != nil || nameExtractor.Name == "" {
		logger.Sugar().Errorf("Payload must be JSON and contain a 'name' field")
		http.Error(w, "Payload must be JSON and contain a 'name' field", http.StatusBadRequest)
		return
	}

	correlationId := uuid.New().String()
	policyUpdate := &pb.PolicyUpdate{
		Type: "agreementUpdate",
		RequestMetadata: &pb.RequestMetadata{
			DestinationQueue: "policyEnforcer-in",
			CorrelationId:    correlationId,
		},
		AgreementName:    nameExtractor.Name,
		AgreementPayload: body,
	}

	resChan := make(chan *pb.PolicyUpdate, 1)
	agreementUpdateMutex.Lock()
	agreementUpdateMap[correlationId] = resChan
	agreementUpdateMutex.Unlock()

	c.SendPolicyUpdate(r.Context(), policyUpdate)

	select {
	case res := <-resChan:
		if res.ValidationResponse != nil && !res.ValidationResponse.RequestApproved {
			http.Error(w, "Policy update rejected by Policy Enforcer", http.StatusBadRequest)
			return
		}
		go checkJobs(nameExtractor.Name)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	case <-time.After(30 * time.Second):
		agreementUpdateMutex.Lock()
		delete(agreementUpdateMap, correlationId)
		agreementUpdateMutex.Unlock()
		http.Error(w, "Timeout waiting for Policy Enforcer validation", http.StatusGatewayTimeout)
	}
}

func updateEtc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Your original updateEtc code here.
		go registerPolicyEnforcerConfiguration()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Updated all config"))
	}
}
