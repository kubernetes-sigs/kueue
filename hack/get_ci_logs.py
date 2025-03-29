import argparse
from bs4 import BeautifulSoup
import requests
import os
from urllib.parse import urljoin, urlparse


def extract_timestamp_from_url(base_url):
    """
    Extracts a timestamp or identifier from the given URL's path.

    This function parses the URL and retrieves the last part of the path,
    which is assumed to be a timestamp or unique identifier. If the URL
    does not contain a valid path, it defaults to the current timestamp.

    Args:
        base_url (str): The base URL to extract the timestamp or identifier from.

    Returns:
        str: The extracted timestamp or identifier. If the URL path is empty,
             it returns the current timestamp in 'YYYYMMDDHHMMSS' format.
    """
    parsed_url = urlparse(base_url)
    path_parts = parsed_url.path.strip("/").split("/")
    return path_parts[-1]

def download_files(base_url, logs_dir, root_url):
    """
    Recursively download all files and folders from the given HTTP root.

    Args:
        base_url (str): The base URL to start downloading from.
        logs_dir (str): The local directory to save the files and folders.
        root_url (str): The root URL to ensure we don't traverse outside the intended hierarchy.
    """
    try:
        # Fetch the content of the URL
        response = requests.get(base_url, timeout=30)
        response.raise_for_status()
    except requests.exceptions.RequestException as e:
        print(f"Failed to fetch {base_url}: {e}")
        return

    # Parse the HTML content
    soup = BeautifulSoup(response.text, "html.parser")

    # Ensure the local directory exists
    os.makedirs(logs_dir, exist_ok=True)

    # Find all links in the page
    links = soup.find_all("a")

    # Skip the link if it appears to be a "link back"
    filtered_links = filter(lambda link: ".." not in link.text, links)

    for link in filtered_links:
        href = link.get("href")
        if not href:
            continue  # Skip empty links

        # Construct the full URL and local path
        full_url = urljoin(base_url, href)
        parsed_url = urlparse(full_url)

        # Determine if it's a directory or a file
        if href.endswith("/"):  # It's a directory
            print(f"Entering directory: {full_url}")
            dir_name = href.split("/")[-2]
            download_files(full_url, os.path.join(logs_dir, dir_name), root_url)
        else:  # It's a file
            print(f"Downloading file: {full_url}")
            file_name = parsed_url.path.split("/")[-1]
            file_path = os.path.join(logs_dir, file_name)
            print(f"Path to save file: {file_path}")
            try:
                file_response = requests.get(full_url, stream=True, timeout=30)
                file_response.raise_for_status()
                with open(file_path, "wb") as f:
                    for chunk in file_response.iter_content(chunk_size=8192):
                        f.write(chunk)
            except requests.exceptions.RequestException as e:
                print(f"Failed to download {full_url}: {e}")

if __name__ == "__main__":
    """
    Entry point for the script.

    This script recursively downloads all files and directories from a specified HTTP URL,
    saving them locally while maintaining the original directory structure.
    It ensures that downloads stay within the specified root URL to avoid unintended traversal
    and creates a timestamped directory to organize the downloaded content.
    To get the URL go to you Pull Request and click on the check that interests you and copy link to the Artifacts.
    E.g.:
    python hack/get_ci_logs.py https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/4568/pull-kueue-test-integration-multikueue-main/1900509723589873664/
    """
    # Parse command-line arguments
    parser = argparse.ArgumentParser(
        description="Recursively download files from a given URL."
    )
    parser.add_argument("url", help="The URL to start downloading from.")
    args = parser.parse_args()

    # URL to start downloading from
    url = args.url

    # Extract the timestamp or fallback to current time
    timestamp_dir = extract_timestamp_from_url(url)

    # Determine the directory one level above the script's location
    script_dir = os.path.dirname(os.path.abspath(__file__))
    parent_dir = os.path.dirname(script_dir)
    logs_dir = os.path.join(parent_dir, "build_logs", timestamp_dir)

    # Root URL to restrict traversal
    root_url = url  # Set the root URL to the base URL to prevent going up

    # Start downloading
    download_files(url, logs_dir, root_url)
