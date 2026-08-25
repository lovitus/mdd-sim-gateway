import os
from pathlib import Path
from unittest.mock import patch

import yaml

from control.app import config


def test_config_load_cache_is_copy_safe_and_invalidates_on_replace_and_save(tmp_path):
    path = tmp_path / "config.yaml"
    path.write_text("instances: {}\nsettings:\n  timezone: Asia/Shanghai\n",
                    encoding="utf-8")
    original = yaml.safe_load
    with patch.multiple(
            config, DATA_DIR=str(tmp_path), CONFIG_PATH=str(path),
            _load_cache_key=None, _load_cache_value=None), \
            patch.object(config.yaml, "safe_load", wraps=original) as parse:
        first = config.load()
        first["settings"]["timezone"] = "mutated-only-in-caller"
        second = config.load()
        assert second["settings"]["timezone"] == "Asia/Shanghai"
        assert parse.call_count == 1

        replacement = path.with_suffix(".replacement")
        replacement.write_text(
            "instances: {}\nsettings:\n  timezone: Europe/London\n",
            encoding="utf-8")
        os.replace(replacement, path)
        assert config.load()["settings"]["timezone"] == "Europe/London"
        assert parse.call_count == 2

        saved = config.load()
        saved["settings"]["timezone"] = "Asia/Singapore"
        config.save(saved)
        assert config.load()["settings"]["timezone"] == "Asia/Singapore"
        assert parse.call_count == 3
        assert (Path(config.CONFIG_PATH).stat().st_mode & 0o777) == 0o600


def test_config_cache_tracks_symlink_target_inode_not_link_inode(tmp_path):
    first_target = tmp_path / "target.yaml"
    first_target.write_text(
        "instances: {}\nsettings:\n  timezone: Asia/Shanghai\n",
        encoding="utf-8")
    link = tmp_path / "config.yaml"
    link.symlink_to(first_target)
    original = yaml.safe_load
    with patch.multiple(
            config, DATA_DIR=str(tmp_path), CONFIG_PATH=str(link),
            _load_cache_key=None, _load_cache_value=None), \
            patch.object(config.yaml, "safe_load", wraps=original) as parse:
        assert config.load()["settings"]["timezone"] == "Asia/Shanghai"
        replacement = tmp_path / "target.replacement"
        replacement.write_text(
            "instances: {}\nsettings:\n  timezone: Europe/Paris\n",
            encoding="utf-8")
        os.replace(replacement, first_target)
        assert link.is_symlink()
        assert config.load()["settings"]["timezone"] == "Europe/Paris"
        assert parse.call_count == 2
