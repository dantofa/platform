import typer

from dantofa.core import greeting, meta

app = typer.Typer(help="dantofa command line utility.")


def _version_callback(value: bool) -> None:
    if not value:
        return
    typer.echo(meta.resolve_version())
    raise typer.Exit


@app.command()
def hello(
    name: str = "world",
    _: bool = typer.Option(
        False,
        "--version",
        callback=_version_callback,
        is_eager=True,
        help="Show the version and exit.",
    ),
) -> None:
    """Say hello."""
    typer.echo(greeting.greet(name))


def run() -> None:
    app()


if __name__ == "__main__":
    run()
