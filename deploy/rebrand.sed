# Steward branding, applied by deploy/rebrand.sh to a copy of the sources while the
# image is built: upstream files stay untouched in git, so upstream merges never
# conflict. Extended regex (sed -E). Order matters: specific phrases first, then the
# generic rule, then the upstream credit (protected by @@UPSTREAM@@) is restored.

# Attribution kept where the bot describes itself (MIT license of YAGPDB).
s#The bot is run by Botlabs but is open source \(<https://github\.com/botlabs-gg/yagpdb>\), so if you know Go and want to make some contributions, feel free to make a PR\.#Steward is a private bot run by Haxurus, based on the open source @@UPSTREAM@@ by Botlabs (MIT license, <https://github.com/botlabs-gg/yagpdb>).#g
s#YAGPDB is open source! The project is hosted on GitHub here: <a href="https://github\.com/botlabs-gg/yagpdb">botlabs-gg/yagpdb</a>\.#Steward is based on the open source project <a href="https://github.com/botlabs-gg/yagpdb">@@UPSTREAM@@</a> by Botlabs (MIT license).#g

# Tagline.
s#YAGPDB - Yet Another General Purpose Discord Bot#Steward - Discord server management by Haxurus#g
s#Yet Another General Purpose Discord Bot#Discord server management by Haxurus#g

# User agents sent to third-party APIs identify this instance, not the official one.
s#YAGPDB\.xyz \(\+https://yagpdb\.xyz; https://github\.com/botlabs-gg/yagpdb\)#Steward (+https://github.com/haxurus/steward)#g
s#YAGPDB\.xyz \(https://github\.com/botlabs-gg/yagpdb\)#Steward (+https://github.com/haxurus/steward)#g
s#YAGPDB\.xyz#Steward#g

# Example links to the official instance's public pages.
s#https?://yagpdb\.xyz/public/#/public/#g
s#alt="logo of yagpdb"#alt="Steward logo"#g

# Every remaining standalone YAGPDB (not YAGPDB_ env names), twice for adjacent hits.
s#(^|[^A-Za-z0-9_])YAGPDB([^A-Za-z0-9_]|$)#\1Steward\2#g
s#(^|[^A-Za-z0-9_])YAGPDB([^A-Za-z0-9_]|$)#\1Steward\2#g

s#@@UPSTREAM@@#YAGPDB#g
