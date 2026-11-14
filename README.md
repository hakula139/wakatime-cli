# WakaTime CLI

[![CI](https://img.shields.io/github/actions/workflow/status/hakula139/wakatime-cli/on_push.yml?branch=develop&label=ci)](https://github.com/hakula139/wakatime-cli/actions)
[![Coverage](https://img.shields.io/codecov/c/gh/hakula139/wakatime-cli/develop)](https://app.codecov.io/gh/hakula139/wakatime-cli)

This fork maintains custom fixes to [WakaTime CLI](https://github.com/wakatime/wakatime-cli). Build it from source using [Contributing][contributing].

Command line interface to [WakaTime][wakatime] used by all WakaTime [text editor plugins][editors].

Go to [http://wakatime.com/editors][editors] to install the plugin for your text editor or IDE.

## Usage

Normally you don't need to use wakatime-cli directly unless you're building a new WakaTime plugin.
If you're building a plugin using the [WakaTime API][api], follow the [Creating a Plugin][creating-plugin] guide.

WakaTime plugins and wakatime-cli share a common [INI][usage] config file:

`$WAKATIME_HOME/.wakatime.cfg`

Without an explicit config path or `WAKATIME_HOME`, wakatime-cli also supports an
[XDG config fallback](USAGE.md#config-file) when `~/.wakatime.cfg` does not exist.

IDE plugins log to the IDE’s console, and wakatime-cli writes JSONL logs to:

`$WAKATIME_HOME/.wakatime/wakatime.log`

Set `debug = true` in your config file to enable verbose logs.

See [Usage][usage] or the [WakaTime FAQ][faq] for more details.

## Contributing

Pull requests and issues are welcome!
See [Contributing][contributing] for more details.

## Troubleshooting

See [Troubleshooting][troubleshooting] for more details.

Many thanks to all [contributors][authors]!

Made with :heart: by the WakaTime Team.

[wakatime]: http://wakatime.com
[editors]: http://wakatime.com/editors
[api]: https://wakatime.com/developers/
[creating-plugin]: https://wakatime.com/help/misc/creating-plugin
[faq]: https://wakatime.com/faq
[usage]: USAGE.md
[contributing]: CONTRIBUTING.md
[troubleshooting]: TROUBLESHOOTING.md
[authors]: AUTHORS
