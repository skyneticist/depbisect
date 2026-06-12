package cli

import (
	"fmt"
	"io"
)

const bashCompletion = `# bash completion for depbisect
_depbisect() {
    local cur prev words
    cur="${COMP_WORDS[COMP_CWORD]}"
    case "${COMP_WORDS[1]}" in
        run)
            COMPREPLY=( $(compgen -W "--base --to --repo --runs --run-timeout --pm --report-md --report-json --no-reports --keep-worktrees --dry-run --verbose --" -- "$cur") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh" -- "$cur") )
            ;;
        *)
            COMPREPLY=( $(compgen -W "run version completion help" -- "$cur") )
            ;;
    esac
}
complete -F _depbisect depbisect
`

const zshCompletion = `#compdef depbisect
_depbisect() {
    local -a commands
    commands=(
        'run:bisect dependency updates'
        'version:print version'
        'completion:generate shell completion'
        'help:show usage'
    )
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case "$words[2]" in
        run)
            _arguments \
                '--base[base revision]:rev:' \
                '--to[target revision]:rev:' \
                '--repo[repository path]:path:_directories' \
                '--runs[runs per candidate]:n:' \
                '--run-timeout[timeout per run]:duration:' \
                '--pm[package manager]:pm:(npm pnpm)' \
                '--report-md[markdown report path]:path:_files' \
                '--report-json[json report path]:path:_files' \
                '--no-reports[write no reports]' \
                '--keep-worktrees[keep temporary worktree]' \
                '--dry-run[plan only]' \
                '--verbose[verbose output]'
            ;;
        completion)
            _values 'shell' bash zsh
            ;;
    esac
}
_depbisect "$@"
`

func completionMain(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "depbisect: usage: depbisect completion bash|zsh")
		return ExitError
	}
	switch args[0] {
	case "bash":
		fmt.Fprint(stdout, bashCompletion)
		return ExitOK
	case "zsh":
		fmt.Fprint(stdout, zshCompletion)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "depbisect: unsupported shell %q (supported: bash, zsh)\n", args[0])
		return ExitError
	}
}
