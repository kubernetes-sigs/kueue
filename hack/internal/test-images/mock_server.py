import os
from fastapi import FastAPI, WebSocket, WebSocketDisconnect, Request
from fastapi.responses import JSONResponse
import uvicorn
import multiprocessing
import asyncio
import logging
import atexit

logging.basicConfig(level=logging.INFO)

HEALTHZ_RESPONSE_TEXT = "success"
CURRENT_VERSION = "4" #special internal ray version literal
RAY_VERSION = os.getenv("RAY_VERSION", "2.41.0")

# In-memory storage for jobs (to simulate job state)
jobs = {}

def create_app():
    """
    Creates and configures the main FastAPI application.

    Returns:
        FastAPI: The configured FastAPI application instance.
    """
    app = FastAPI()

    @app.get("/api/local_raylet_healthz")
    def local_raylet_healthz():
        """
        Health check endpoint for the local Raylet.

        Returns:
            str: A success message indicating the health of the local Raylet.
        """
        return HEALTHZ_RESPONSE_TEXT

    @app.get("/api/gcs_healthz")
    def gcs_healthz():
        """
        Health check endpoint for the GCS (Global Control Store).

        Returns:
            str: A success message indicating the health of the GCS.
        """
        return HEALTHZ_RESPONSE_TEXT
    
    @app.get("/api/jobs/{job_id}")
    def get_job_status(job_id: str):
        """
        Retrieves the status of a specific job.

        Args:
            job_id (str): The ID of the job to retrieve.

        Returns:
            dict: The job status and metadata if found.
            JSONResponse: A 404 error if the job is not found.
        """
        if job_id in jobs:
            return {
                "job_id": job_id,
                "status": jobs[job_id]["status"],
                "type": "SUBMISSION",
                "entrypoint": jobs[job_id]["entrypoint"],
            }
        return JSONResponse(status_code=404, content={"error": "Job not found"})

    @app.get("/api/jobs/{job_id}/logs")
    def get_job_logs(job_id: str):
        """
        Retrieves the logs of a specific job.

        Args:
            job_id (str): The ID of the job to retrieve logs for.

        Returns:
            dict: The job logs if found.
            JSONResponse: A 404 error if the job is not found.
        """
        if job_id in jobs:
            return {"job_id": job_id, "logs": jobs[job_id]["logs"]}
        return JSONResponse(status_code=404, content={"error": "Job not found"})

    @app.post("/api/jobs")
    async def submit_job(request: Request):
        """
        Submits a new job for execution.

        Args:
            request (Request): The HTTP request containing the job submission payload.

        Returns:
            dict: A success message and job metadata if submission is valid.
            JSONResponse: A 400 error if the submission payload is invalid.
        """
        data = await request.json()
        job_id = data.get("submission_id")
        entrypoint = data.get("entrypoint")
        if not job_id or not entrypoint:
            return JSONResponse(status_code=400, content={"error": "Invalid job submission payload"})

        jobs[job_id] = {
            "status": "RUNNING",
            "logs": f"Executing: {entrypoint}",
            "entrypoint": entrypoint,
        }
        return {"job_id": job_id, "submission_id": job_id}
    
    @app.get("/api/version")
    def get_version():
        """
        Retrieves the current version of the application and Ray.

        Returns:
            dict: The application version and Ray version.
        """
        return {"version": CURRENT_VERSION, "ray_version": RAY_VERSION}

    @app.delete("/api/jobs/{job_id}")
    def stop_job(job_id: str):
        """
        Stops a running job.

        Args:
            job_id (str): The ID of the job to stop.

        Returns:
            dict: A success message if the job was stopped.
            JSONResponse: A 404 error if the job is not found.
        """
        if job_id in jobs:
            jobs[job_id]["status"] = "STOPPED"
            return {"success": True}
        return JSONResponse(status_code=404, content={"error": "Job not found"})

    @app.websocket("/api/jobs/{job_id}/logs/tail")
    async def tail_job_logs(websocket: WebSocket, job_id: str):
        """
        Streams the logs of a specific job via WebSocket.

        Args:
            websocket (WebSocket): The WebSocket connection.
            job_id (str): The ID of the job to stream logs for.

        Returns:
            None
        """
        await websocket.accept()
        if job_id in jobs:
            try:
                for i in range(2):
                    await websocket.send_text(f"Log line {i + 1} for job {job_id}")
                    await asyncio.sleep(0.5)
                await websocket.send_text(f"Job {job_id} completed.")
            except WebSocketDisconnect:
                print(f"WebSocket disconnected for job {job_id}")
            finally:
                jobs[job_id]["status"] = "SUCCEEDED"
        else:
            await websocket.send_text(f"Job {job_id} not found.")
        await websocket.close()

    return app

def run_server(port, path: str):
    """
    Runs a FastAPI server for health check endpoints.

    Args:
        port (int): The port to run the server on.
        path (str): The health check endpoint path.

    Returns:
        None
    """
    app = FastAPI()

    @app.get(path)
    def healthz():
        """
        Health check endpoint.

        Returns:
            str: A success message indicating the health of the server.
        """
        return HEALTHZ_RESPONSE_TEXT

    uvicorn.run(app, host="0.0.0.0", port=port)

def ray_startup_hook(ray_params, head):
    """
    Hook triggered during Ray startup to initialize FastAPI servers.

    Args:
        ray_params (dict): Parameters for Ray startup.
        head (bool): Indicates if the current node is the head node.

    Returns:
        None
    """
    print("Ray startup hook triggered!")

    if head:
        p1 = multiprocessing.Process(target=run_server, args=(52365, "/api/local_raylet_healthz"))
        p2 = multiprocessing.Process(target=uvicorn.run, args=(create_app(),),
                                      kwargs={"host": "0.0.0.0", "port": 8265})
        p1.start()
        p2.start()

        def cleanup():
            """
            Cleans up FastAPI servers during shutdown.

            Returns:
                None
            """
            print("Cleaning up FastAPI servers...")
            p1.terminate()
            p2.terminate()
            p1.join()
            p2.join()

        atexit.register(cleanup)
    else:
        p1 = multiprocessing.Process(target=run_server, args=(52365, "/api/local_raylet_healthz"))
        p1.start()

        def cleanup():
            """
            Cleans up FastAPI servers during shutdown.

            Returns:
                None
            """
            print("Cleaning up FastAPI servers...")
            p1.terminate()
            p1.join()

        atexit.register(cleanup)

    print("FastAPI servers started on ports 52365 and 8265.")
    