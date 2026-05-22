"""Tests for parse_config() in benchmark_data/under_tested.py."""
import unittest
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from under_tested import parse_config


class TestParseConfigValid(unittest.TestCase):
    """Valid configurations."""

    def test_valid_full_config(self):
        result = parse_config('{"host": "localhost", "port": 8080}')
        self.assertEqual(result, {"host": "localhost", "port": 8080})

    def test_valid_with_extra_fields(self):
        result = parse_config('{"host": "example.com", "port": 443, "debug": true}')
        self.assertEqual(result, {"host": "example.com", "port": 443, "debug": True})

    def test_valid_port_lowest(self):
        result = parse_config('{"host": "a", "port": 1}')
        self.assertEqual(result, {"host": "a", "port": 1})

    def test_valid_port_large(self):
        result = parse_config('{"host": "a", "port": 65535}')
        self.assertEqual(result, {"host": "a", "port": 65535})


class TestParseConfigEmpty(unittest.TestCase):
    """Empty / whitespace-only input."""

    def test_empty_string(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config("")
        self.assertIn("empty config", str(ctx.exception))

    def test_whitespace_only(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config("   ")
        self.assertIn("empty config", str(ctx.exception))

    def test_newline_only(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config("\n\n")
        self.assertIn("empty config", str(ctx.exception))


class TestParseConfigMalformed(unittest.TestCase):
    """Malformed JSON input."""

    def test_invalid_json(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config("{bad}")
        self.assertIn("malformed JSON", str(ctx.exception))

    def test_truncated_json(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"host": "localhost"')
        self.assertIn("malformed JSON", str(ctx.exception))

    def test_not_json(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config("just a string")
        self.assertIn("malformed JSON", str(ctx.exception))

    def test_number_as_json(self):
        """JSON int is valid — but not iterable for dict lookup -> TypeError."""
        with self.assertRaises(TypeError):
            parse_config("42")


class TestParseConfigMissingFields(unittest.TestCase):
    """Missing required fields."""

    def test_missing_host(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"port": 8080}')
        self.assertIn("missing required field: host", str(ctx.exception))

    def test_missing_port(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"host": "localhost"}')
        self.assertIn("missing required field: port", str(ctx.exception))

    def test_both_missing(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{}')
        self.assertIn("missing required field: host", str(ctx.exception))

    def test_array_json_missing_fields(self):
        """JSON array is valid JSON but not a dict — missing fields."""
        with self.assertRaises(ValueError) as ctx:
            parse_config('["a", "b"]')
        self.assertIn("missing required field", str(ctx.exception))

    def test_null_json_missing_fields(self):
        """JSON null decodes to None -> not iterable -> TypeError."""
        with self.assertRaises(TypeError):
            parse_config("null")


class TestParseConfigInvalidPort(unittest.TestCase):
    """Invalid port values."""

    def test_port_string(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"host": "localhost", "port": "8080"}')
        self.assertIn("invalid port: 8080", str(ctx.exception))

    def test_port_zero(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"host": "localhost", "port": 0}')
        self.assertIn("invalid port: 0", str(ctx.exception))

    def test_port_negative(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"host": "localhost", "port": -1}')
        self.assertIn("invalid port: -1", str(ctx.exception))

    def test_port_float(self):
        with self.assertRaises(ValueError) as ctx:
            parse_config('{"host": "localhost", "port": 3.14}')
        self.assertIn("invalid port:", str(ctx.exception))

    def test_port_bool(self):
        """bool is a subclass of int in Python — True passes isinstance(int) check."""
        result = parse_config('{"host": "localhost", "port": true}')
        self.assertEqual(result["port"], True)


if __name__ == "__main__":
    unittest.main()
