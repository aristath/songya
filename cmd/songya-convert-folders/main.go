// Explicit offline conversion; never run while an old Songya writer is active.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/aristath/songya"
	"os"
)

func main() {
	path := flag.String("store", "", "Store to convert; stop all old-layout writers first")
	flag.Parse()
	store, err := songya.OpenExisting(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report, err := store.ConvertFolders(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
