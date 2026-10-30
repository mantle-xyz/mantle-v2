package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/triage"
	"github.com/spf13/cobra"
)

var triagePrevious, triageCurrent, triageOutput string

var triageCmd = &cobra.Command{
	Use:   "triage",
	Short: "Compare raw differences in two saved reports",
	RunE: func(_ *cobra.Command, _ []string) error {
		return runTriage(triagePrevious, triageCurrent, triageOutput)
	},
}

func init() {
	triageCmd.Flags().StringVar(&triagePrevious, "previous", "", "Previous JSON report")
	triageCmd.Flags().StringVar(&triageCurrent, "current", "", "Current JSON report")
	triageCmd.Flags().StringVar(&triageOutput, "output", "triage.json", "Triage JSON output path")
	rootCmd.AddCommand(triageCmd)
}

func runTriage(previousPath, currentPath, outputPath string) error {
	if previousPath == "" || currentPath == "" || outputPath == "" {
		return fmt.Errorf("--previous, --current, and --output are required")
	}
	previous, err := readTriageReport(previousPath)
	if err != nil {
		return fmt.Errorf("previous report: %w", err)
	}
	current, err := readTriageReport(currentPath)
	if err != nil {
		return fmt.Errorf("current report: %w", err)
	}
	summary, err := triage.Compare(previous, current)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode triage summary: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write triage summary: %w", err)
	}
	fmt.Printf("Triage: %d new, %d changed, %d resolved, %d not run, %d uncompared, %d unchanged. Saved to %s\n",
		len(summary.New), len(summary.Changed), len(summary.Resolved), len(summary.NotRun),
		len(summary.Uncompared), summary.Unchanged, outputPath)
	return nil
}

func readTriageReport(path string) (*report.Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var value report.Report
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON: %v", err)
	}
	if value.SchemaVersion < 3 || value.Results == nil {
		return nil, fmt.Errorf("unsupported or incomplete RPC compatibility report")
	}
	return &value, nil
}
