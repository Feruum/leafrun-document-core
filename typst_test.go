package documentcore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRenderCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Render(ctx, RenderOptions{ProjectDir: testTempDir(t)})
	requireErrorKind(t, err, ErrorCanceled)
	if !errors.Is(err, context.Canceled) || result.PDFPath != "" {
		t.Fatalf("cancellation lost: %#v, %v", result, err)
	}
}

func TestRenderMissingEngine(t *testing.T) {
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "Missing engine\n")
	t.Setenv("PATH", testTempDir(t))
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	requireErrorKind(t, err, ErrorEngine)
	var execErr *exec.Error
	if result.PDFPath != "" || !errors.As(err, &execErr) {
		t.Fatalf("missing-engine cause lost: %#v, %v", result, err)
	}
}

// The helper is a real child process for version, cancellation, and output
// fault cases that cannot be triggered deterministically in the real engine.
// Its source and executable both live under the calling test's TempDir.
func buildFaultEngine(t *testing.T) string {
	t.Helper()
	dir := testTempDir(t)
	name := "typst"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(dir, name)
	source := filepath.Join(dir, "fault_engine.go")
	writeTestFile(t, source, faultEngineSource)
	cmd := exec.Command("go", "build", "-o", executable, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build subprocess helper: %v: %s", err, output)
	}
	return dir
}

func TestRenderEngineAndOutputFaults(t *testing.T) {
	helperDir := buildFaultEngine(t)
	for _, test := range []struct {
		mode string
		kind ErrorKind
	}{
		{"version_mismatch", ErrorEngine},
		{"version_prerelease", ErrorEngine},
		{"version_future_format", ErrorEngine},
		{"version_failure", ErrorEngine},
		{"compile_failure", ErrorCompile},
		{"invalid_pdf", ErrorStorage},
		{"empty_pdf", ErrorStorage},
		{"missing_pdf", ErrorStorage},
		{"missing_dependencies", ErrorStorage},
		{"empty_dependencies", ErrorStorage},
	} {
		t.Run(test.mode, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "Fault test\n")
			output := filepath.Join(root, "output/result.pdf")
			old := "%PDF-existing output\n"
			writeTestFile(t, output, old)
			t.Setenv("PATH", helperDir)
			t.Setenv("LEAFRUN_FAULT_MODE", test.mode)
			result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
			requireErrorKind(t, err, test.kind)
			if result.PDFPath != "" {
				t.Fatalf("fault published %q", result.PDFPath)
			}
			if strings.HasPrefix(test.mode, "version_") && !strings.Contains(err.Error(), "typst") {
				t.Fatalf("version error lost engine output: %v", err)
			}
			if test.mode == "compile_failure" || test.mode == "version_failure" {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("original subprocess error lost: %v", err)
				}
			}
			data, readErr := os.ReadFile(output)
			if readErr != nil || string(data) != old {
				t.Fatalf("fault changed old output: %v", readErr)
			}
			requireNoRenderTemps(t, filepath.Dir(output))
		})
	}
	for _, mode := range []string{"raw_success", "version_stderr"} {
		t.Run(mode, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "Raw text\n")
			t.Setenv("PATH", helperDir)
			t.Setenv("LEAFRUN_FAULT_MODE", mode)
			result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "raw_success" && (!strings.Contains(result.RawOutput, "unparsed stdout") || !strings.Contains(result.RawOutput, "future engine stderr")) {
				t.Fatalf("unparsed output was discarded: %#v", result)
			}
			if mode == "version_stderr" && !strings.Contains(result.RawOutput, "future version stderr") {
				t.Fatalf("version stderr was discarded: %#v", result)
			}
			requirePDF(t, result.PDFPath)
			requireNoRenderTemps(t, filepath.Dir(result.PDFPath))
		})
	}
	for _, mode := range []string{"cancel_version", "cancel_compile"} {
		t.Run(mode, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "Cancel child\n")
			output := filepath.Join(root, "output/result.pdf")
			old := "%PDF-old before cancellation\n"
			writeTestFile(t, output, old)
			ready := filepath.Join(root, "child.ready")
			t.Setenv("PATH", helperDir)
			t.Setenv("LEAFRUN_FAULT_MODE", mode)
			t.Setenv("LEAFRUN_FAULT_READY", ready)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result RenderResult
				err    error
			}
			finished := make(chan outcome, 1)
			go func() {
				result, err := Render(ctx, RenderOptions{ProjectDir: root})
				finished <- outcome{result, err}
			}()
			deadline := time.After(10 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case got := <-finished:
					t.Fatalf("render exited before child became ready: %v", got.err)
				case <-deadline:
					cancel()
					t.Fatal("child did not become ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			cancel()
			select {
			case got := <-finished:
				requireErrorKind(t, got.err, ErrorCanceled)
				if got.result.PDFPath != "" || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("cancellation lost: %#v, %v", got.result, got.err)
				}
				var exitErr *exec.ExitError
				if !errors.As(got.err, &exitErr) {
					t.Fatalf("killed child exit cause lost: %v", got.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("render did not kill and wait for the child")
			}
			data, readErr := os.ReadFile(output)
			if readErr != nil || string(data) != old {
				t.Fatalf("canceled render changed old output: %v", readErr)
			}
			requireNoRenderTemps(t, filepath.Dir(output))
		})
	}
	t.Run("version_checked_every_invocation", func(t *testing.T) {
		root := testTempDir(t)
		writeTestFile(t, filepath.Join(root, "main.typ"), "Version check\n")
		t.Setenv("PATH", helperDir)
		t.Setenv("LEAFRUN_FAULT_MODE", "raw_success")
		first, err := Render(context.Background(), RenderOptions{ProjectDir: root})
		if err != nil {
			t.Fatal(err)
		}
		old := requirePDF(t, first.PDFPath)
		t.Setenv("LEAFRUN_FAULT_MODE", "version_mismatch")
		second, err := Render(context.Background(), RenderOptions{ProjectDir: root})
		requireErrorKind(t, err, ErrorEngine)
		if second.PDFPath != "" {
			t.Fatal("mismatched engine published output")
		}
		data, readErr := os.ReadFile(first.PDFPath)
		if readErr != nil || string(data) != string(old) {
			t.Fatal("version failure changed existing output")
		}
	})
}

const faultEngineSource = `package main
import ("fmt"; "os"; "strings"; "time")
func arg(args []string, key string) string { for i, a := range args { if a == key && i+1<len(args) { return args[i+1] } }; return "" }
func block() { _ = os.WriteFile(os.Getenv("LEAFRUN_FAULT_READY"), []byte("ready"), 0600); time.Sleep(time.Minute) }
func main() {
 mode := os.Getenv("LEAFRUN_FAULT_MODE")
 args := os.Args[1:]
 if len(args)==1 && args[0]=="--version" {
  switch mode {
  case "version_mismatch": fmt.Println("typst 0.15.10 (test)")
  case "version_prerelease": fmt.Println("typst 0.15.1-dev (test)")
  case "version_future_format": fmt.Println("typst-next version 0.15.1")
  case "version_failure": fmt.Fprintln(os.Stderr,"typst version failed"); os.Exit(17)
  case "cancel_version": block()
  case "version_stderr": fmt.Println("typst 0.15.1 (test)"); fmt.Fprintln(os.Stderr,"future version stderr")
  default: fmt.Println("typst 0.15.1 (test)")
  }; return
 }
 if len(args)<5 || args[0]!="--color" || args[1]!="never" || args[2]!="compile" || arg(args,"--format")!="pdf" || arg(args,"--root")=="" || arg(args,"--diagnostic-format")!="human" || arg(args,"--deps-format")!="zero" {
  fmt.Fprintln(os.Stderr,"error: invalid compile arguments"); os.Exit(90)
 }
 entry, output := args[len(args)-2], args[len(args)-1]
 deps := arg(args,"--deps")
 if deps=="" || !strings.HasSuffix(output,".pdf") { fmt.Fprintln(os.Stderr,"error: missing staging path"); os.Exit(91) }
 _ = os.WriteFile(deps, append([]byte(entry),0),0600)
 switch mode {
 case "compile_failure": _ = os.WriteFile(output,[]byte("%PDF-partial"),0600); fmt.Fprintln(os.Stderr,"error: helper compile failure"); os.Exit(23)
 case "cancel_compile": _ = os.WriteFile(output,[]byte("%PDF-partial"),0600); block(); return
 case "invalid_pdf": _ = os.WriteFile(output,[]byte("not a PDF"),0600); return
 case "empty_pdf": _ = os.WriteFile(output,nil,0600); return
 case "missing_pdf": _ = os.Remove(output); return
 case "missing_dependencies": _ = os.Remove(deps)
 case "empty_dependencies": _ = os.WriteFile(deps,nil,0600)
 case "raw_success": fmt.Println("unparsed stdout"); fmt.Fprintln(os.Stderr,"future engine stderr")
 }
 _ = os.WriteFile(output,[]byte("%PDF-1.7\nhelper body\n"),0600)
}
`
