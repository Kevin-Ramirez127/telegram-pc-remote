## Advices to create a safer bot

Apply these settings directly in your chat with @BotFather to minimize the bot's public exposure and capabilities.

    Obfuscate the Username: Choose a random, alphanumeric username (e.g., @x7B_9mP_Home_bot). Since bots cannot be hidden from search, treating the username like a password prevents accidental discovery.

    Disable Group Access: Send /setjoingroups, select your bot, and choose Disable. This prevents unauthorized users from adding your bot to public groups to probe its behavior.

    Enable Privacy Mode: Send /setprivacy, select your bot, and choose Enable. If a vulnerability somehow forces the bot into a group, this setting prevents it from reading standard chat messages.

    Disable Inline Queries: Send /setinline, select your bot, and ensure it is turned off.