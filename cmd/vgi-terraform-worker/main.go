// Command vgi-terraform-worker serves the terraform catalog to DuckDB.
//
//	vgi-terraform-worker                                  # stdio (what DuckDB launches)
//	vgi-terraform-worker --http --host 0.0.0.0 --port 8000  # HTTP, GET /health
//	vgi-terraform-worker --unix /tmp/terraform.sock         # AF_UNIX (launch: transport)
//
// Attach it from DuckDB:
//
//	ATTACH 'terraform' (TYPE vgi, LOCATION '/abs/path/to/bin/vgi-terraform-worker');
package main

import (
	"os"

	"github.com/Query-farm/vgi-go/vgi"

	"github.com/Query-farm/vgi-terraform/internal/serve"
	"github.com/Query-farm/vgi-terraform/internal/terraform"
)

func main() {
	serve.Main("vgi-terraform-worker", os.Args[1:], func() *vgi.Worker { return terraform.NewWorker() })
}
