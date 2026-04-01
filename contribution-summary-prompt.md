# Claude Prompt: Generate Contribution Summary

Use this prompt in any repository where you want to generate a comprehensive contribution summary similar to CONTRIBUTIONS.md in this repository.

---

## Prompt

I'd like you to create a comprehensive summary of all my contributions to this repository. Here's what I need:

**Step 1: Gather My Commits**

First, find all non-merge commits I've made to this repository. My author identities may include:
- [Your name as it appears in commits]
- [Your email address(es)]
- [Any other variations of your GitHub username/email]

Please analyze the git history and identify all unique pull requests associated with my commits.

**Step 2: Organize by Contribution Type**

Rather than listing commits chronologically, I want you to organize my work into thematic categories based on the nature of the contributions. Common categories might include:
- Infrastructure/DevOps/CI/CD work
- API development or protocol changes
- Testing improvements and reliability work
- Build system and tooling
- Security fixes and vulnerability patches
- Documentation
- Developer experience improvements
- Feature development
- Performance optimizations
- Bug fixes

Identify the categories that best represent my work in this repository.

**Step 3: Write the Summary**

For each category, write a comprehensive paragraph (not bullet points) that:
- Uses active voice with my name as the subject (e.g., "Caden implemented..." not "Work was done...")
- Provides context about WHY the work mattered, not just WHAT was changed
- Groups related work together to tell a coherent story
- Includes inline markdown links to pull requests where specific work is mentioned
  - Format: `([#1234](https://github.com/OWNER/REPO/pull/1234))`
- Is written for someone less familiar with the repository internals

**Step 4: Create the File**

Create a file called `CONTRIBUTIONS.md` in the repository root with:
- A title including my name
- An introduction explaining the document's purpose
- One section per contribution category (## heading)
- Each section containing a detailed paragraph as described above
- A footer noting the timespan of contributions

**Style Guidelines:**
- Write in complete sentences and paragraphs, not bullet points
- Be specific about technical details but explain them accessibly
- Every PR reference should be a clickable markdown link
- Use active voice throughout
- Focus on impact and outcomes, not just changes made
- Make it digestible for someone unfamiliar with the codebase

**Example of good writing style:**

> Caden implemented a comprehensive certificate lifecycle management system for Managed Service Identity certificates used in MIWI clusters. He created automated refresh logic that triggers during cluster update, admin update ([#4222](https://github.com/Azure/ARO-RP/pull/4222)), and maintenance operations ([#4390](https://github.com/Azure/ARO-RP/pull/4390)), with intelligent eligibility checks to only rotate certificates within their renewal window.

Please start by gathering my commit information and then proceed with the analysis.

---

## Customization Notes

Before using this prompt in a new repository, update:
1. **Your name/email identities**: Replace the placeholder with your actual git author names and email addresses
2. **Repository context**: The categories will vary based on the type of project (web app vs. CLI tool vs. library, etc.)
3. **PR link format**: Ensure the GitHub org/repo name is correct in the example if you want to show a template

## Tips for Best Results

- If you have many commits (>100), consider asking Claude to focus on merged PRs rather than individual commits
- If the repository uses a different PR numbering system (e.g., Jira tickets), adjust the linking format accordingly
- If commits don't reference PR numbers, ask Claude to organize by commit themes instead
- For very large contribution histories, consider breaking it into time periods (e.g., by year)
