/// <reference types="@raycast/api">

/* 🚧 🚧 🚧
 * This file is auto-generated from the extension's manifest.
 * Do not modify manually. Instead, update the `package.json` file.
 * 🚧 🚧 🚧 */

/* eslint-disable @typescript-eslint/ban-types */

type ExtensionPreferences = {
  /** GitHub Brain Project Path - Path to the github-brain project directory (containing scripts/run) */
  "githubBrainPath": string,
  /** GitHub Organization - The GitHub organization to search (must match the one used in 'github-brain pull') */
  "organization"?: string,
  /** Results Limit - Number of results to display */
  "resultsLimit": "5" | "10" | "20" | "30" | "50",
  /** Auto-open Single Result - Automatically open when only one match found */
  "autoOpen": boolean
}

/** Preferences accessible in all the extension's commands */
declare type Preferences = ExtensionPreferences

declare namespace Preferences {
  /** Preferences accessible in the `search` command */
  export type Search = ExtensionPreferences & {}
}

declare namespace Arguments {
  /** Arguments passed to the `search` command */
  export type Search = {}
}

