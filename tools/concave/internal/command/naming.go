package command

import (
	"encoding/json"
	"fmt"
	"os"

	"concave/internal/artifact"
	"concave/internal/naming"
	"concave/internal/policy"
	"github.com/spf13/cobra"
)

func (a *app) registerNaming(root *cobra.Command) {
	var inputPath string
	cmd := &cobra.Command{Use: "naming", Short: "Check naming against explicit captured source", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var input naming.Input
		if _, err := artifact.Read(inputPath, &input); err != nil {
			return err
		}
		source, err := os.OpenRoot(input.Root)
		if err != nil {
			return err
		}
		defer source.Close()
		body, err := naming.Read(source, "concave.json")
		if err != nil {
			return err
		}
		if naming.Digest(body) != input.PolicySHA256 || input.Files["concave.json"] != input.PolicySHA256 {
			return fmt.Errorf("naming policy differs from captured input")
		}
		p, err := policy.NamingPolicy(body)
		if err != nil {
			return err
		}
		report, runErr := naming.Run(cmd.Context(), input, *p)
		if err := outputNaming(cmd, report); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if report.State == "failed" {
			return &Exit{Code: 1}
		}
		return nil
	}}
	cmd.Flags().StringVar(&inputPath, "input", "", "Closed captured-source input manifest")
	_ = cmd.MarkFlagRequired("input")
	root.AddCommand(cmd)
}
func outputNaming(cmd *cobra.Command, r naming.Report) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
}
