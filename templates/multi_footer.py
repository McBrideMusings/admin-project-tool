

@command("clean", "Remove build artifacts")
def cmd_clean(args):
    info("Cleaning...")
{{CLEAN_BODY}}
    ok("Done.")


{{RELOAD_REGISTRATION}}


if __name__ == "__main__":
    boot(PROJECT_NAME{{BOOT_ARGS}})
