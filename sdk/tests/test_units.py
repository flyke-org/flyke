import pytest

from flyke._internal.units import duration_ms, size_bytes


@pytest.mark.parametrize(
    "text,ms",
    [
        ("200ms", 200),
        ("2s", 2000),
        ("45m", 2_700_000),
        ("24h", 86_400_000),
        ("1h30m", 5_400_000),
        (1.5, 1500),
    ],
)
def test_duration(text, ms):
    assert duration_ms(text) == ms


@pytest.mark.parametrize("bad", ["", "10", "5 parsecs", "1h-2m"])
def test_bad_duration(bad):
    with pytest.raises(ValueError):
        duration_ms(bad)


def test_sizes():
    assert size_bytes("512Mi") == 512 * 1024**2
    assert size_bytes("1G") == 10**9
    assert size_bytes(42) == 42


def test_schedule_triggers():
    from flyke import on

    assert on.every("15m").to_manifest() == {"type": "schedule", "cron": "@every 900s"}
    assert on.schedule(
        "0 3 * * *", tz="Europe/Paris", name="nightly", missed="skip"
    ).to_manifest() == {
        "type": "schedule",
        "cron": "0 3 * * *",
        "tz": "Europe/Paris",
        "name": "nightly",
        "missed": "skip",
    }
    for bad in ("5s", "1500ms"):
        with pytest.raises(ValueError):
            on.every(bad)
    with pytest.raises(ValueError):
        on.schedule("@daily", missed="backfill")
