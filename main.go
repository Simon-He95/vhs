package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strings"
	"syscall"

	version "github.com/hashicorp/go-version"
	"github.com/spf13/cobra"
)

const extension = ".tape"

var (
	// Version stores the build version of VHS at the time of packaging through -ldflags
	//
	// go build -ldflags "-s -w -X=main.Version=$(VERSION)" main.go
	Version string

	// CommitSHA stores the commit SHA of VHS at the time of packaging through -ldflags
	CommitSHA string

	ttydMinVersion = version.Must(version.NewVersion("1.7.2"))

	publish bool
	rootCmd = &cobra.Command{
		Use:           "vhs <file>",
		Short:         "Run a given tape file and generates its outputs.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true, // we print our own errors
		RunE: func(cmd *cobra.Command, args []string) error {
			err := ensureDependencies()
			if err != nil {
				return err
			}

			in := cmd.InOrStdin()
			// Set the input to the file contents if a file is given
			// otherwise, use stdin
			if len(args) > 0 && args[0] != "-" {
				in, err = os.Open(args[0])
				if err != nil {
					return err
				}
				fmt.Println(FileStyle.Render("File: " + args[0]))
			}

			input, err := io.ReadAll(in)
			if err != nil {
				return err
			}
			if string(input) == "" {
				return errors.New("no input provided")
			}

			var output string
			errs := Evaluate(cmd.Context(), string(input), os.Stdout, func(v *VHS) {
				output = v.Options.Video.Output.GIF
			})
			if len(errs) > 0 {
				printErrors(os.Stderr, string(input), errs)
				return errors.New("recording failed")
			}

			if publish && output != "" {
				url, err := Publish(cmd.Context(), output)
				if err != nil {
					return err
				}
				fmt.Println(StringStyle.Render("URL: " + url))
			}

			return nil
		},
	}

	markdown  bool
	themesCmd = &cobra.Command{
		Use:   "themes",
		Short: "List all the available themes, one per line",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var prefix, suffix string
			if markdown {
				fmt.Fprintf(cmd.OutOrStdout(), "# Themes\n\n")
				prefix, suffix = "* `", "`"
			}
			themes, err := sortedThemeNames()
			if err != nil {
				return err
			}
			for _, theme := range themes {
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s%s\n", prefix, theme, suffix)
			}
			return nil
		},
	}

	shell     string
	recordCmd = &cobra.Command{
		Use:   "record",
		Short: "Create a new tape file by recording your actions",
		Args:  cobra.NoArgs,
		RunE:  Record,
	}

	newCmd = &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new tape file with example tape file contents and documentation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fileName := strings.TrimSuffix(args[0], extension) + extension

			f, err := os.Create(fileName)
			if err != nil {
				return err
			}

			_, err = f.Write(DemoTape)
			if err != nil {
				return err
			}

			fmt.Println("Created " + fileName)

			return nil
		},
	}

	validateCmd = &cobra.Command{
		Use:   "validate <file>...",
		Short: "Validate a glob file path and parses all the files to ensure they are valid without running them.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			valid := true

			for _, file := range args {

				b, err := os.ReadFile(file)
				if err != nil {
					continue
				}

				l := NewLexer(string(b))
				p := NewParser(l)

				_ = p.Parse()
				errs := p.Errors()

				if len(errs) != 0 {
					fmt.Println(ErrorFileStyle.Render(file))

					for _, err := range errs {
						printParserError(os.Stderr, string(b), err)
					}
					valid = false
				}
			}

			if !valid {
				return errors.New("invalid tape file(s)")
			}

			return nil
		},
	}
)

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt, syscall.SIGTERM,
	)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolVarP(&publish, "publish", "p", false, "publish your GIF to vhs.charm.sh and get a shareable URL")
	themesCmd.Flags().BoolVar(&markdown, "markdown", false, "output as markdown")
	_ = themesCmd.Flags().MarkHidden("markdown")
	recordCmd.Flags().StringVarP(&shell, "shell", "s", "bash", "shell for recording")
	rootCmd.AddCommand(
		recordCmd,
		newCmd,
		themesCmd,
		validateCmd,
		manCmd,
		serveCmd,
		publishCmd,
	)
	rootCmd.CompletionOptions.HiddenDefaultCmd = true

	if len(CommitSHA) >= 7 { //nolint:gomnd
		vt := rootCmd.VersionTemplate()
		rootCmd.SetVersionTemplate(vt[:len(vt)-1] + " (" + CommitSHA[0:7] + ")\n")
	}
	if Version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Sum != "" {
			Version = info.Main.Version
		} else {
			Version = "unknown (built from source)"
		}
	}
	rootCmd.Version = Version
}

var versionRegex = regexp.MustCompile(`\d+\.\d+\.\d+`)

// getVersion returns the parsed version of a program
func getVersion(program string) *version.Version {
	cmd := exec.Command(program, "--version")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	programVersion, _ := version.NewVersion(versionRegex.FindString(string(out)))
	return programVersion
}

// ensureDependencies ensures that all dependencies are correctly installed
// and versioned before continuing
func ensureDependencies() error {
	_, ffmpegErr := exec.LookPath("ffmpeg")
	if ffmpegErr != nil {
		return fmt.Errorf("ffmpeg is not installed. Install it from: http://ffmpeg.org")
	}
	_, ttydErr := exec.LookPath("ttyd")
	if ttydErr != nil {
		return fmt.Errorf("ttyd is not installed. Install it from: https://github.com/tsl0922/ttyd")
	}
	_, bashErr := exec.LookPath("bash")
	if bashErr != nil {
		return fmt.Errorf("bash is not installed")
	}

	ttydVersion := getVersion("ttyd")
	if ttydVersion == nil || ttydVersion.LessThan(ttydMinVersion) {
		return fmt.Errorf("ttyd version (%s) is out of date, VHS requires %s\n%s",
			ttydVersion,
			ttydMinVersion,
			"Install the latest version from: https://github.com/tsl0922/ttyd")
	}

	return nil
}
