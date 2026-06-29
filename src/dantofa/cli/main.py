import typer

from dantofa.commands.digitalocean.app import do_app
from dantofa.core import meta

app = typer.Typer(help="dantofa command line utility.")
# Registered under both names: `do` is short/ergonomic, `digitalocean` is the
# long alias for shells/CI where `do` is a reserved word (shellcheck SC1010).
app.add_typer(do_app, name="do")
app.add_typer(do_app, name="digitalocean", hidden=True)


def _version_callback(value: bool) -> None:
    if not value:
        return
    typer.echo(meta.resolve_version())
    raise typer.Exit


@app.callback()
def main(
    _: bool = typer.Option(
        False,
        "--version",
        callback=_version_callback,
        is_eager=True,
        help="Show the version and exit.",
    ),
) -> None:
    """dantofa command line utility."""


def run() -> None:
    app()


if __name__ == "__main__":
    run()
