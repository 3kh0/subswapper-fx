// launch-provider is an offline provider fixture for launch diagnostics.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "auth" && os.Args[2] == "status" {
		switch os.Getenv("DIAGNOSTIC_TEST_AUTH") {
		case "auth-failed":
			fmt.Fprintln(os.Stderr, "SYNTHETIC-PRIVATE-PROVIDER-ERROR")
			os.Exit(7)
		case "auth-rejected":
			fmt.Println(`{"loggedIn":false,"error":"SYNTHETIC-PRIVATE-PROVIDER-ERROR"}`)
		case "auth-malformed":
			fmt.Println("SYNTHETIC-PRIVATE-PROVIDER-ERROR")
		default:
			fmt.Println(`{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`)
		}
		return
	}
	fmt.Println("provider task executed")
}
