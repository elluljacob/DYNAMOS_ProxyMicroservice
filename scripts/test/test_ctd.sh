#!/bin/bash
echo "Local computeToData (VU) - 10k rows"
mkdir -p logs
for i in 1 2 3 4 5 6 7 8 9 10; do
  echo "Run $i:"
  time curl -s --location 'http://api-gateway.api-gateway.svc.cluster.local:80/api/v1/requestApproval' \
  --header 'Content-Type: application/json' \
  --data-raw '{
      "type": "sqlDataRequest",
      "user": {"id": "1", "userName": "jorrit.stutterheim@cloudnation.nl"},
      "dataProviders": ["VU"],
      "data_request": {
          "type": "sqlDataRequest",
          "query": "SELECT * FROM Personen p JOIN Aanstellingen s LIMIT 10000",
          "algorithm": "average",
          "options": {"graph": false, "aggregate": false},
          "requestMetadata": {}
      }
  }' | tee logs/local_compute_10k_run${i}.json
  echo ""
  sleep 5
done