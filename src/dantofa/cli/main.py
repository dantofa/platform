from importlib.metadata import PackageNotFoundError
from importlib.metadata import version as _pkg_version

import typer

app = typer.Typer(help="dantofa command line utility.")


def _version_callback(value: bool) -> None:
    if not value:
        return
    try:
        resolved = _pkg_version("dantofa-cli")
    except PackageNotFoundError:  # not installed (e.g. running from source tree)
        resolved = "0.0.0+unknown"
    typer.echo(resolved)
    raise typer.Exit


@app.command()
def hello(
    name: str = "world",
    version: bool = typer.Option(
        False,
        "--version",
        callback=_version_callback,
        is_eager=True,
        help="Show the version and exit.",
    ),
) -> None:
    """Say hello."""
    typer.echo(f"Hello, {name}!")


def run() -> None:
    app()


if __name__ == "__main__":
    run()
