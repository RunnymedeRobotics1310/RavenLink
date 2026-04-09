"""Tests for FMSState bitmask parsing."""

import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from src.nt_client import FMSState


class TestFMSStateParsing:
    """Test FMSState.from_raw with known bitmask values."""

    def test_all_zeros(self):
        s = FMSState.from_raw(0x00)
        assert not s.enabled
        assert not s.auto_mode
        assert not s.test_mode
        assert not s.estop
        assert not s.fms_attached
        assert not s.ds_attached
        assert s.raw == 0x00

    def test_enabled_bit(self):
        s = FMSState.from_raw(0x01)
        assert s.enabled
        assert not s.auto_mode

    def test_auto_mode_bit(self):
        s = FMSState.from_raw(0x02)
        assert not s.enabled
        assert s.auto_mode

    def test_test_mode_bit(self):
        s = FMSState.from_raw(0x04)
        assert s.test_mode

    def test_estop_bit(self):
        s = FMSState.from_raw(0x08)
        assert s.estop

    def test_fms_attached_bit(self):
        s = FMSState.from_raw(0x10)
        assert s.fms_attached

    def test_ds_attached_bit(self):
        s = FMSState.from_raw(0x20)
        assert s.ds_attached

    def test_auto_enabled_fms_attached(self):
        """0x13 = enabled + auto + FMS attached."""
        s = FMSState.from_raw(0x13)
        assert s.enabled
        assert s.auto_mode
        assert not s.test_mode
        assert not s.estop
        assert s.fms_attached
        assert not s.ds_attached

    def test_teleop_enabled_fms_attached(self):
        """0x11 = enabled + FMS attached (teleop — no auto, no test)."""
        s = FMSState.from_raw(0x11)
        assert s.enabled
        assert not s.auto_mode
        assert not s.test_mode
        assert s.fms_attached

    def test_disabled_fms_attached(self):
        """0x10 = disabled + FMS attached."""
        s = FMSState.from_raw(0x10)
        assert not s.enabled
        assert s.fms_attached

    def test_all_bits_set(self):
        """0x3F = all 6 bits set."""
        s = FMSState.from_raw(0x3F)
        assert s.enabled
        assert s.auto_mode
        assert s.test_mode
        assert s.estop
        assert s.fms_attached
        assert s.ds_attached

    def test_disconnected_state(self):
        s = FMSState.disconnected()
        assert not s.enabled
        assert not s.fms_attached
        assert s.raw == -1

    def test_auto_start_match(self):
        """Typical auto start: enabled + auto + FMS + DS = 0x33."""
        s = FMSState.from_raw(0x33)
        assert s.enabled
        assert s.auto_mode
        assert s.fms_attached
        assert s.ds_attached
        assert not s.test_mode
        assert not s.estop

    def test_teleop_with_ds(self):
        """Typical teleop: enabled + FMS + DS = 0x31."""
        s = FMSState.from_raw(0x31)
        assert s.enabled
        assert not s.auto_mode
        assert s.fms_attached
        assert s.ds_attached

    def test_str_representation(self):
        s = FMSState.from_raw(0x13)
        text = str(s)
        assert "ENABLED" in text
        assert "AUTO" in text
        assert "FMS" in text

    def test_disconnected_str(self):
        s = FMSState.disconnected()
        assert "DISCONNECTED" in str(s)
