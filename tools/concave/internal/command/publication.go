package command

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"concave/internal/landing"
	"concave/internal/output"
	"concave/internal/review"
	"concave/internal/workflow"
)

func (a *app) printPublication(p *workflow.Publication) error {
	root, err := filepath.Abs(a.repoPath)
	if err != nil {
		return err
	}
	next := p.Next.InRepository(root)
	copy := *p
	copy.Next = next
	if a.jsonMode {
		return output.JSON(a.out, &copy)
	}
	fmt.Fprintln(a.out, p.Headline())
	fmt.Fprintln(a.out, "Contribution:", p.Contribution)
	if p.Commit != "" {
		fmt.Fprintln(a.out, "Commit:", p.Commit)
	}
	output.Next(a.out, next)
	return nil
}

func (a *app) registerPublication(parent *cobra.Command) {
	var message, resolution string
	publish := &cobra.Command{Use: "publish <contribution>", Short: "Publish a verified contribution through land.py, or resume its publication", Args: cobra.ExactArgs(1), Annotations: map[string]string{"timeout": "policy"}, RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		r, err := review.Read(repo, args[0])
		if err != nil {
			return err
		}
		p, err := a.workflow(repo).Publish(r, message, resolution)
		if p != nil {
			if e := a.printPublication(p); e != nil {
				return e
			}
		}
		return err
	}}
	publish.Flags().StringVarP(&message, "message", "m", "", "concrete message, required on first publication")
	publish.Flags().StringVar(&resolution, "resolve", "", "private conflict directory resolved for this publication")
	parent.AddCommand(publish)
	parent.AddCommand(&cobra.Command{Use: "status <contribution>", Short: "Show a contribution's verdict or publication status", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		r, err := review.Read(repo, args[0])
		if err != nil {
			return err
		}
		if r.Candidate != nil {
			p, err := a.workflow(repo).Publication(r)
			if err != nil {
				return err
			}
			if p.State != landing.Unpublished {
				return a.printPublication(p)
			}
		}
		return a.inspectContribution(repo, r)
	}})
}
